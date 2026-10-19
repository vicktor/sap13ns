package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"sap13ns/internal/store"
)

// Autosave and edit locks. The editor sends a heartbeat every few seconds
// while open: it renews the user's soft lock on the post and, when there
// are unsaved changes, stores them as the user's autosave. Another user who
// opens the post sees who is editing and may take over.

const lockTTL = 2 * time.Minute

type heartbeatReq struct {
	Title    *string `json:"title"`
	Content  *string `json:"content"`
	Excerpt  *string `json:"excerpt"`
	Takeover bool    `json:"takeover"`
}

type heartbeatResp struct {
	SavedAt  *time.Time `json:"saved_at,omitempty"`
	LockedBy string     `json:"locked_by,omitempty"` // another user holds the lock
}

func (s *Server) editablePostByID(r *http.Request) (*store.Post, bool) {
	id, ok := pathID(r)
	if !ok {
		return nil, false
	}
	p, err := s.store.PostByID(r.Context(), id)
	if err != nil || p.Status == store.StatusTrash || !canEditPost(currentUser(r), p) {
		return nil, false
	}
	return p, true
}

func (s *Server) apiHeartbeat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.editablePostByID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var req heartbeatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	u := currentUser(r)
	ctx := r.Context()
	var resp heartbeatResp
	holder, got, err := s.store.AcquireLock(ctx, p.ID, u.ID, lockTTL, req.Takeover)
	if err != nil {
		s.log.Error("edit lock", "post", p.ID, "err", err)
	} else if !got {
		resp.LockedBy = holder.Name
	}
	if req.Content != nil && req.Title != nil && req.Excerpt != nil {
		if len(*req.Title) > 400 || len(*req.Excerpt) > 2000 || len(*req.Content) > 2_000_000 {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too large"})
			return
		}
		at, err := s.store.SaveAutosave(ctx, store.Autosave{PostID: p.ID, UserID: u.ID, Title: *req.Title,
			Content: *req.Content, Excerpt: *req.Excerpt})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		resp.SavedAt = &at
	}
	writeJSON(w, http.StatusOK, resp)
}

// apiAutosave returns the user's autosave of a post, for "Restore".
func (s *Server) apiAutosave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.editablePostByID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	a, err := s.store.GetAutosave(r.Context(), p.ID, uid(r))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if r.Method == http.MethodDelete {
		s.store.DeleteAutosave(r.Context(), p.ID, uid(r))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// apiUnlock releases the lock when the editor closes (sendBeacon).
func (s *Server) apiUnlock(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(r); ok {
		s.store.ReleaseLock(r.Context(), id, uid(r))
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminPostTakeover takes the edit lock from another user.
func (s *Server) adminPostTakeover(w http.ResponseWriter, r *http.Request) {
	p, ok := s.editablePostByID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	if holder, err := s.store.LockHolderOf(r.Context(), p.ID); err == nil && holder.UserID != uid(r) {
		s.audit(r, uid(r), "post.lock_taken", "#"+strconv.FormatInt(p.ID, 10), map[string]string{"from": holder.Name})
	}
	if _, _, err := s.store.AcquireLock(r.Context(), p.ID, uid(r), lockTTL, true); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(p.ID, 10), http.StatusSeeOther)
}

// editState is what the editor shows about locks and autosaves.
type editState struct {
	LockedBy    string    // another user is editing
	AutosaveAt  time.Time // newer autosave than the stored version
	HasAutosave bool
	Version     string // updated_at of the loaded version, for conflict checks
}

func (s *Server) loadEditState(r *http.Request, p *store.Post) editState {
	st := editState{Version: strconv.FormatInt(p.UpdatedAt.UnixMicro(), 10)}
	ctx := r.Context()
	u := currentUser(r)
	if holder, got, err := s.store.AcquireLock(ctx, p.ID, u.ID, lockTTL, false); err == nil && !got {
		st.LockedBy = holder.Name
	}
	if a, err := s.store.GetAutosave(ctx, p.ID, u.ID); err == nil {
		if a.SavedAt.After(p.UpdatedAt) && (a.Content != p.Content || a.Title != p.Title || a.Excerpt != p.Excerpt) {
			st.HasAutosave, st.AutosaveAt = true, a.SavedAt
		} else {
			s.store.DeleteAutosave(ctx, p.ID, u.ID)
		}
	}
	return st
}

// editConflict reports who changed the post since the editor loaded it.
func (s *Server) editConflict(r *http.Request, p *store.Post) (string, bool) {
	v := r.FormValue("version")
	if v == "" || v == strconv.FormatInt(p.UpdatedAt.UnixMicro(), 10) {
		return "", false
	}
	name, err := s.store.LastEditor(r.Context(), p.ID)
	if err != nil || name == "" {
		name = s.tu(r, "someone else")
	}
	return name, true
}

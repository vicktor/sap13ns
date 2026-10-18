package main

import (
	"fmt"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// handle answers comment.check and the comment.approved event. Neither
// ever returns an error: the host holds a comment whose check failed, so
// comment.check always answers with an explicit verdict, "ok" included. A
// problem (storage or StopForumSpam unavailable) is explained in the log
// while the local checks still decide.
func handle(req *pluginsdk.Request) (any, error) {
	cfg := ConfigFrom(req.Plugin.Settings)
	for _, w := range cfg.Warnings {
		pluginsdk.Log(pluginsdk.Warn, w)
	}
	switch {
	case req.Hook == pluginsdk.HookCommentCheck:
		var c pluginsdk.CommentCheck
		if err := req.Decode(&c); err != nil {
			pluginsdk.Log(pluginsdk.Warn, "comment.check: unreadable payload: "+err.Error())
			return pluginsdk.Verdict(verdictOK, ""), nil
		}
		v, reason := check(req, cfg, c)
		return pluginsdk.Verdict(v, reason), nil
	case req.Hook == pluginsdk.HookEvent && req.Event == pluginsdk.EventCommentApproved:
		approved(req)
	}
	return nil, nil
}

// check classifies a new comment. The host logs held and spam comments
// itself ("comment.check: pending by …"); accepted ones are logged here
// when log_ok is on.
func check(req *pluginsdk.Request, cfg Config, c pluginsdk.CommentCheck) (string, string) {
	known := false
	if !c.Registered {
		var err error
		if known, err = isKnown(req.Site.URL, c.Author); err != nil {
			pluginsdk.Log(pluginsdk.Warn, "comment.check: the list of known commenters is not available ("+err.Error()+"); treating the commenter as new")
		}
	}
	signals := Check(Comment{Author: c.Author, URL: c.URL, Content: c.Content, Email: c.Email, Registered: c.Registered, Known: known}, cfg)
	verdict, reason := Decide(signals)

	// StopForumSpam only for comments that look clean: a slow answer can
	// use up the host's two-second budget, and the host then discards the
	// whole answer and holds the comment, so a local "spam" is never
	// downgraded to "pending".
	var notes []string
	if cfg.SFS && verdict == verdictOK {
		if _, ok := SFSQuery(cfg, c.IP, c.Email); !ok {
			notes = append(notes, "StopForumSpam not asked: no public IP address or e-mail address to look up")
		} else {
			switch s, err := AskSFS(cfg, c.IP, c.Email); {
			case err != nil:
				pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("comment.check by %q: %v; judged by the local checks only", short(c.Author), err))
				notes = append(notes, "StopForumSpam not available")
			case s != nil:
				verdict, reason = Decide([]Signal{*s})
			default:
				notes = append(notes, "not listed by StopForumSpam")
			}
		}
	}
	if verdict == verdictOK && cfg.LogOK {
		pluginsdk.Log(pluginsdk.Info, fmt.Sprintf("comment.check: ok by %q: %s", short(c.Author), okReason(c, known, notes)))
	}
	return verdict, reason
}

// okReason says why a comment passed, for the log.
func okReason(c pluginsdk.CommentCheck, known bool, notes []string) string {
	parts := []string{"no spam signals"}
	switch links := len(linkRe.FindAllStringIndex(c.Content, -1)); {
	case links == 1:
		parts = append(parts, "1 link")
	case links > 1:
		parts = append(parts, fmt.Sprintf("%d links", links))
	}
	switch {
	case c.Registered:
		parts = append(parts, "signed-in user")
	case known:
		parts = append(parts, "known commenter")
	default:
		parts = append(parts, "first comment")
	}
	return strings.Join(append(parts, notes...), "; ")
}

// approved remembers the author of an approved comment, so their next
// comments with links are not held as first comments.
func approved(req *pluginsdk.Request) {
	var ev pluginsdk.CommentEvent
	if err := req.Decode(&ev); err != nil {
		pluginsdk.Log(pluginsdk.Warn, "comment.approved: unreadable payload: "+err.Error())
		return
	}
	isNew, err := learn(req.Site.URL, ev.Author)
	switch {
	case err != nil:
		pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("comment %d approved: could not remember %q (%v)", ev.ID, short(ev.Author), err))
	case isNew:
		pluginsdk.Log(pluginsdk.Info, fmt.Sprintf("comment %d approved: %q is now a known commenter", ev.ID, short(ev.Author)))
	}
}

// short truncates a name for the log, as the host does (60 runes).
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

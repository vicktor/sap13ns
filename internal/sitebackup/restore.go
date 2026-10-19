package sitebackup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sap13ns/internal/auth"
	"sap13ns/internal/backup"
	"sap13ns/internal/builder"
	"sap13ns/internal/customize"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
	"sap13ns/internal/wpimport"
)

// RestoreOptions restore a backup into a site.
type RestoreOptions struct {
	// Options are the site's own: where its backups go (the safety backup,
	// and the decrypted copy of an encrypted archive), its uploads, its
	// address, the passphrase.
	Options
	// Replace allows replacing content the site has. Without it a restore
	// only goes into an empty site.
	Replace bool
	// ActorID is the administrator restoring: never demoted or disabled,
	// and the author of content whose author is unknown. 0 on the command
	// line: the first administrator of the backup then.
	ActorID int64
	// CheckRedirect applies the rules of the redirects page to a restored
	// redirect (st is the restore's transaction, where the restored posts
	// are), returning it normalised or an error to leave it out. Required.
	CheckRedirect func(ctx context.Context, st *store.Store, mainLang, source, target string, status int) (string, string, error)
}

// RestoreReport says what a restore did.
type RestoreReport struct {
	Manifest *Manifest
	Safety   *Archive // the backup of what the restore replaced; nil for an empty site

	Posts, Pages, Media, Comments, Terms int
	UsersCreated, UsersMatched           int
	MenuItems, Widgets, Designs          int
	Redirects, RedirectsRefused, Plugins int
	Warnings                             []string
}

func (r *RestoreReport) warn(format string, args ...any) {
	if len(r.Warnings) < 500 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
	}
}

// sidecar is what sap13ns/ holds.
type sidecar struct {
	settings   sideSettings
	users      []sideUser
	media      []store.Media
	menus      []store.MenuItem
	widgets    []store.Widget
	appearance sideAppearance
	redirects  []store.Redirect
	plugins    []sidePlugin
}

// Restore replaces the content of the site of st with the backup in file:
// posts, pages, comments, terms, media and their files, menus, widgets,
// designs and layouts, redirects, settings, theme options and the plain
// settings of plugins the site has; users are matched by username (or
// e-mail) and created when missing, never deleted. Everything is checked
// before the site changes; a site with content (or files) is backed up
// first, and needs Replace. The database changes in one transaction and
// the uploads directory is swapped in only then: a restore that fails
// leaves the site as it was.
func Restore(ctx context.Context, st *store.Store, file string, o RestoreOptions) (*RestoreReport, error) {
	if o.CheckRedirect == nil || o.UploadsDir == "" {
		return nil, errors.New("sitebackup: a restore needs the redirect check and the uploads directory")
	}
	unlock, err := lock(o.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, err := open(file, o.Passphrase, o.Dir)
	if err != nil {
		return nil, err
	}
	defer r.close()
	rep := &RestoreReport{Manifest: r.m}
	side, err := readSidecar(r)
	if err != nil {
		return nil, err
	}

	has, err := st.HasContent(ctx)
	if err != nil {
		return nil, err
	}
	if !has {
		has, err = hasFiles(o.UploadsDir)
		if err != nil {
			return nil, err
		}
	}
	if has && !o.Replace {
		return nil, ErrNotEmpty
	}
	if has {
		a, _, err := create(ctx, st, o.Options, true)
		if err != nil {
			return nil, fmt.Errorf("the safety backup failed, nothing was restored: %w", err)
		}
		rep.Safety = a
	}

	// The files, into a directory next to the uploads.
	if err := os.MkdirAll(filepath.Dir(o.UploadsDir), 0o750); err != nil {
		return nil, err
	}
	staging := filepath.Join(filepath.Dir(o.UploadsDir), "."+filepath.Base(o.UploadsDir)+".restore-"+randomSuffix())
	if err := os.Mkdir(staging, uploadsMode(o.UploadsDir)); err != nil {
		return nil, err
	}
	swapped := false
	defer func() {
		if !swapped {
			os.RemoveAll(staging)
		}
	}()
	rows, err := stage(ctx, r, staging, side.media, rep)
	if err != nil {
		return nil, err
	}

	exp, err := readWXR(r)
	if err != nil {
		return nil, err
	}

	tx, err := st.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	ts := st.WithTx(tx)
	if err := ts.WipeContent(ctx); err != nil {
		return nil, err
	}
	sameSite := r.m.Site.Network != "" && r.m.Site.Network == o.Site.Network && r.m.Site.ID == o.Site.ID
	users, logins, err := restoreUsers(ctx, ts, side.users, o.ActorID, sameSite, rep)
	if err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}
	author := o.ActorID
	if author == 0 {
		author = firstAdmin(side.users, users)
	}
	if author == 0 {
		return nil, errors.New("the backup has no administrator and nobody restores it")
	}

	sink := &restoreSink{st: ts, users: users, logins: logins, author: author, mainLang: r.m.Site.Language,
		media: map[string]*store.Media{}, posts: map[int64]bool{}, terms: map[int64]bool{}, comments: map[int64]bool{}}
	for i := range rows {
		m := &rows[i]
		m.UploaderID = mapUser(users, m.UploaderID)
		if err := ts.RestoreMedia(ctx, m); err != nil {
			return nil, fmt.Errorf("media %s: %w", m.Path, err)
		}
		sink.media[m.Path] = m
	}
	rep.Media = len(rows)

	langs := append([]string{r.m.Site.Language}, r.m.Site.Langs...)
	ir, err := wpimport.Import(ctx, exp, sink, wpimport.Options{
		DownloadMedia: true, IncludeTrash: true, KeepPrivate: true, KeepAddresses: true,
		DefaultAuthorID: author, MaxMediaBytes: maxFileBytes, MaxMediaFiles: maxFiles,
		Language: r.m.Site.Language, Languages: langs,
		PostURL: func(typ, lang, slug string) string {
			p := "/blog/" + slug
			if typ == wpimport.TypePage {
				p = "/" + slug
			}
			if lang != "" && lang != r.m.Site.Language {
				p = "/" + lang + p
			}
			return p
		},
	})
	if err != nil {
		return nil, err
	}
	c := ir.Counts
	rep.Posts, rep.Pages, rep.Comments, rep.Terms = c.Posts.Created, c.Pages.Created, c.Comments.Created, c.Categories.Created+c.Tags.Created
	if c.Posts.Failed+c.Pages.Failed > 0 {
		return nil, fmt.Errorf("%d posts and pages could not be restored: %s", c.Posts.Failed+c.Pages.Failed,
			strings.Join(ir.Warnings[:min(len(ir.Warnings), 5)], "; "))
	}
	for _, w := range ir.Warnings {
		rep.warn("%s", w)
	}

	if err := applySidecar(ctx, ts, side, users, r.m, o, rep); err != nil {
		return nil, err
	}
	if err := ts.ResetIdentities(ctx); err != nil {
		return nil, err
	}

	old, err := swapIn(staging, o.UploadsDir)
	if err != nil {
		return nil, fmt.Errorf("uploads: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		if berr := swapBack(staging, o.UploadsDir, old); berr != nil {
			return nil, fmt.Errorf("%w; putting the previous uploads back failed: %v (they are in %s)", err, berr, old)
		}
		return nil, err
	}
	swapped = true
	if old != "" {
		os.RemoveAll(old) // a directory this restore named, next to the uploads
	}
	return rep, nil
}

func readSidecar(r *reader) (*sidecar, error) {
	s := &sidecar{}
	for _, f := range []struct {
		name string
		v    any
	}{
		{nameSettings, &s.settings}, {nameUsers, &s.users}, {nameMedia, &s.media}, {nameMenus, &s.menus},
		{nameWidgets, &s.widgets}, {nameAppear, &s.appearance}, {nameRedirect, &s.redirects}, {namePlugins, &s.plugins},
	} {
		if err := r.readJSON(f.name, f.v); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func readWXR(r *reader) (*wpimport.Export, error) {
	rc, err := r.member(NameWXR)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	exp, err := wpimport.Parse(wpimport.LimitReader(rc, maxWXRBytes))
	if err != nil {
		return nil, fmt.Errorf("export.xml: %w", err)
	}
	// The parser may stop at </rss>: the rest must still match the checksum.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return nil, err
	}
	return exp, nil
}

// hasFiles reports whether dir holds any regular file.
func hasFiles(dir string) (bool, error) {
	found := errors.New("found")
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			return found
		}
		return nil
	})
	if errors.Is(err, found) {
		return true, nil
	}
	return false, err
}

func uploadsMode(dir string) fs.FileMode {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return fi.Mode().Perm()
	}
	return 0o755
}

// stage extracts the files of the media library into dir, checking each
// as an upload is checked: a type uploads accept, detected from the
// content, matching the media item's type and the file's extension.
// Media items whose files are missing or fail are left out, with a
// warning; files no media item names are not restored.
func stage(ctx context.Context, r *reader, dir string, rows []store.Media, rep *RestoreReport) ([]store.Media, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	x := &extractor{root: root, dirs: map[string]bool{}}
	var kept []store.Media
	seenPath, seenID := map[string]bool{}, map[int64]bool{}
	for _, m := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files := []string{m.Path}
		for _, k := range slices.Sorted(maps.Keys(m.Variants)) {
			files = append(files, m.Variants[k].Path)
		}
		why := ""
		switch {
		case m.ID <= 0 || seenID[m.ID]:
			why = "invalid or repeated id"
		case media.Allowed[m.MIME] == "":
			why = "type " + m.MIME + " is not accepted"
		}
		for _, p := range files {
			if why != "" {
				break
			}
			switch {
			case !backup.SafeName(p) || strings.HasPrefix(p, ".") || strings.Contains(p, "/."):
				why = "unsafe path"
			case seenPath[p]:
				why = "a file of another media item"
			case !media.MatchesName(m.MIME, p):
				why = "file name does not match its type"
			case !r.has(DirUploads + p):
				why = p + " is missing from the backup"
			default:
				if err := x.file(r, DirUploads+p, p, m.MIME); err != nil {
					if errors.Is(err, backup.ErrCorrupt) {
						return nil, err
					}
					why = err.Error()
				}
			}
		}
		if why != "" {
			for _, p := range files {
				if backup.SafeName(p) && !seenPath[p] {
					root.Remove(p)
				}
			}
			rep.warn("media %d (%s) not restored: %s", m.ID, m.OriginalName, why)
			continue
		}
		seenID[m.ID] = true
		for _, p := range files {
			seenPath[p] = true
		}
		kept = append(kept, m)
	}
	return kept, nil
}

type extractor struct {
	root *os.Root
	dirs map[string]bool
}

// file writes member as rel, and checks that its content is of type mime.
func (x *extractor) file(r *reader, member, rel, mime string) error {
	if dir := path.Dir(rel); dir != "." {
		if err := x.mkdirs(dir); err != nil {
			return err
		}
	}
	rc, err := r.member(member)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := x.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	head := make([]byte, 512)
	n, rerr := io.ReadFull(rc, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		f.Close()
		return rerr
	}
	head = head[:n]
	_, err = f.Write(head)
	if err == nil {
		_, err = io.Copy(f, rc)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := media.Sniff(head); got != mime {
		return fmt.Errorf("its content is %s, not %s", got, mime)
	}
	return nil
}

func (x *extractor) mkdirs(dir string) error {
	if x.dirs[dir] {
		return nil
	}
	if parent := path.Dir(dir); parent != "." && parent != dir {
		if err := x.mkdirs(parent); err != nil {
			return err
		}
	}
	if err := x.root.Mkdir(dir, 0o755); err != nil {
		if fi, serr := x.root.Lstat(dir); !errors.Is(err, fs.ErrExist) || serr != nil || !fi.IsDir() {
			return err
		}
	}
	x.dirs[dir] = true
	return nil
}

// swapIn moves staging to target, keeping the previous target aside
// (returned, "" if there was none) until the caller removes it or puts it
// back with swapBack. Both are renames within one directory.
func swapIn(staging, target string) (old string, err error) {
	switch _, err := os.Lstat(target); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	default:
		old = filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".old-"+randomSuffix())
		if err := os.Rename(target, old); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if old != "" {
			if rerr := os.Rename(old, target); rerr != nil {
				return "", fmt.Errorf("%w; the previous uploads are in %s (moving them back failed: %v)", err, old, rerr)
			}
		}
		return "", err
	}
	syncDir(filepath.Dir(target))
	return old, nil
}

func swapBack(staging, target, old string) error {
	if err := os.Rename(target, staging); err != nil {
		return err
	}
	if old != "" {
		return os.Rename(old, target)
	}
	return nil
}

// restoreUsers matches the users of the backup with the site's, by
// username and then e-mail address, and creates the missing ones without
// a password (or, on the same network and site, linked to their network
// account). It returns the new ids by old id and by username.
func restoreUsers(ctx context.Context, st *store.Store, list []sideUser, actor int64, sameSite bool, rep *RestoreReport) (map[int64]int64, map[string]int64, error) {
	ids, logins := map[int64]int64{}, map[string]int64{}
	for _, u := range list {
		if !auth.ValidRole(u.Role) {
			rep.warn("user %q had an unknown role %q; restored as a subscriber", u.Username, u.Role)
			u.Role = auth.RoleSubscriber
		}
		if u.Status != "active" && u.Status != "disabled" {
			u.Status = "active"
		}
		ex, err := st.UserByUsername(ctx, u.Username)
		if errors.Is(err, store.ErrNotFound) && u.Email != "" {
			ex, err = st.UserByEmail(ctx, strings.ToLower(u.Email))
		}
		switch {
		case err == nil:
			role, status := u.Role, u.Status
			if ex.ID == actor || ex.Linked() {
				// The network decides a linked user's role; the
				// administrator restoring keeps theirs.
				role, status = "", ""
			}
			if err := st.RestoreUserProfile(ctx, ex.ID, u.DisplayName, u.Bio, u.Website, role, status); err != nil {
				return nil, nil, err
			}
			ids[u.ID], logins[u.Username] = ex.ID, ex.ID
			rep.UsersMatched++
			continue
		case !errors.Is(err, store.ErrNotFound):
			return nil, nil, err
		}
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if !strings.Contains(email, "@") {
			email = u.Username + "@restore.invalid"
		}
		nu := store.RestoredUser{Username: u.Username, Email: email, DisplayName: u.DisplayName, Bio: u.Bio, Website: u.Website,
			Role: u.Role, Status: u.Status, CreatedAt: u.CreatedAt}
		if u.NetworkAccountID != nil && sameSite {
			nu.NetworkAccountID, nu.NetworkOperator = u.NetworkAccountID, u.NetworkOperator
		}
		id, err := st.CreateRestoredUser(ctx, nu)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", u.Username, err)
		}
		ids[u.ID], logins[u.Username] = id, id
		rep.UsersCreated++
	}
	return ids, logins, nil
}

func firstAdmin(list []sideUser, ids map[int64]int64) int64 {
	for _, u := range list {
		if u.Role == auth.RoleAdmin && u.Status == "active" && ids[u.ID] != 0 {
			return ids[u.ID]
		}
	}
	for _, u := range list {
		if ids[u.ID] != 0 {
			return ids[u.ID]
		}
	}
	return 0
}

func mapUser(ids map[int64]int64, old *int64) *int64 {
	if old == nil {
		return nil
	}
	if id, ok := ids[*old]; ok {
		return &id
	}
	return nil
}

var themeSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// applySidecar puts back what only sap13ns has, after the content.
func applySidecar(ctx context.Context, st *store.Store, side *sidecar, users map[int64]int64, m *Manifest, o RestoreOptions, rep *RestoreReport) error {
	for _, it := range side.menus {
		if !builder.SafeURL(it.URL) {
			rep.warn("menu item %q left out: its address is not allowed", it.Label)
			continue
		}
		if err := st.RestoreMenuItem(ctx, it); err != nil {
			return fmt.Errorf("menu item %q: %w", it.Label, err)
		}
		rep.MenuItems++
	}
	for _, w := range side.widgets {
		if err := st.RestoreWidget(ctx, w); err != nil {
			return fmt.Errorf("widget %q: %w", w.Title, err)
		}
		rep.Widgets++
	}
	for _, c := range side.appearance.Custom {
		limit := customize.MaxJSON
		if c.Kind == store.CustomLayout {
			limit = builder.MaxJSON
		}
		if !themeSlug.MatchString(c.Theme) || (c.Kind != store.CustomDesign && c.Kind != store.CustomLayout) ||
			len(c.Published) > limit || len(c.Draft) > limit || (len(c.Published) > 0 && !json.Valid(c.Published)) ||
			(len(c.Draft) > 0 && !json.Valid(c.Draft)) {
			rep.warn("%s of theme %q left out: it is not valid", c.Kind, c.Theme)
			continue
		}
		// Designs and layouts are normalised again whenever they are
		// loaded (customize.Load, builder.Load), as stored ones always are.
		if err := st.RestoreThemeCustom(ctx, store.ThemeCustom{Theme: c.Theme, Kind: c.Kind, Published: c.Published, Draft: c.Draft,
			PublishedAt: c.PublishedAt, DraftAt: c.DraftAt, UpdatedBy: mapUser(users, c.UpdatedBy)}); err != nil {
			return fmt.Errorf("%s of theme %s: %w", c.Kind, c.Theme, err)
		}
		rep.Designs++
	}
	for _, rv := range side.appearance.Revisions {
		if !themeSlug.MatchString(rv.Theme) || len(rv.Data) > builder.MaxJSON || !json.Valid(rv.Data) {
			continue
		}
		rv.UserID = mapUser(users, rv.UserID)
		if err := st.RestoreThemeCustomRevision(ctx, rv); err != nil {
			return fmt.Errorf("revision of theme %s: %w", rv.Theme, err)
		}
	}
	for _, rd := range side.redirects {
		src, dst, err := o.CheckRedirect(ctx, st, m.Site.Language, rd.Source, rd.Target, rd.Status)
		if err != nil {
			rep.RedirectsRefused++
			rep.warn("redirect %s left out: %v", rd.Source, err)
			continue
		}
		rd.Source, rd.Target = src, dst
		added, err := st.RestoreRedirect(ctx, rd)
		if err != nil {
			return fmt.Errorf("redirect %s: %w", rd.Source, err)
		}
		if added {
			rep.Redirects++
		} else {
			rep.RedirectsRefused++
		}
	}
	return applySettings(ctx, st, side, rep)
}

func applySettings(ctx context.Context, st *store.Store, side *sidecar, rep *RestoreReport) error {
	ss := side.settings
	if len(ss.Site) > 0 {
		set := store.DefaultSettings()
		if err := json.Unmarshal(ss.Site, &set); err != nil {
			return fmt.Errorf("%w: settings: %v", backup.ErrCorrupt, err)
		}
		if err := st.SaveSettings(ctx, set); err != nil {
			return err
		}
	}
	if len(ss.AI) > 0 {
		cur, err := st.AISettings(ctx)
		if err != nil {
			return err
		}
		next := cur
		if err := json.Unmarshal(ss.AI, &next); err != nil {
			return fmt.Errorf("%w: AI settings: %v", backup.ErrCorrupt, err)
		}
		next.APIKeyEnc = cur.APIKeyEnc // keys are never in a backup
		if next.Enabled && len(next.APIKeyEnc) == 0 {
			rep.warn("the writing assistant is on, but API keys are not in backups: enter the key again in Settings → AI")
		}
		if err := st.SaveAISettings(ctx, next); err != nil {
			return err
		}
	}
	if len(ss.Assistant) > 0 {
		cur, err := st.AssistantSettings(ctx)
		if err != nil {
			return err
		}
		next := cur
		if err := json.Unmarshal(ss.Assistant, &next); err != nil {
			return fmt.Errorf("%w: assistant settings: %v", backup.ErrCorrupt, err)
		}
		next.EmbedKeyEnc, next.MCPTokenHash = cur.EmbedKeyEnc, cur.MCPTokenHash
		if ss.MCPToken && next.MCPTokenHash == "" && next.MCPEnabled {
			// The backup had a token for /mcp, which a backup never holds:
			// do not open the endpoint without one.
			next.MCPEnabled = false
			rep.warn("the MCP endpoint was switched off: its token is not in backups; set a new one in Settings → Assistant")
		}
		if err := st.SaveAssistantSettings(ctx, next); err != nil {
			return err
		}
	}
	for theme, opts := range ss.ThemeOptions {
		if !themeSlug.MatchString(theme) {
			continue
		}
		if err := st.SaveThemeOptions(ctx, theme, opts); err != nil {
			return err
		}
	}
	recs, err := st.ListPluginRecords(ctx)
	if err != nil {
		return err
	}
	for _, p := range side.plugins {
		rec, ok := recs[p.ID]
		if !ok {
			rep.warn("plugin %s %s is not installed here; install it to use its settings", p.ID, p.Version)
			continue
		}
		merged := maps.Clone(rec.Settings)
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, plainSettings(p.ID, p.Settings)) // secrets and e-mail addresses stay as they are here
		if err := st.SavePluginSettings(ctx, p.ID, merged); err != nil {
			return err
		}
		rep.Plugins++
	}
	return nil
}

// restoreSink writes the content of export.xml with the ids it had, so
// that the sidecar's references (the front page, menus, designs and
// layouts) hold. The importer converts and checks as for any import.
type restoreSink struct {
	st                     *store.Store
	users                  map[int64]int64
	logins                 map[string]int64
	author                 int64
	mainLang               string
	media                  map[string]*store.Media // restored, by path
	posts, terms, comments map[int64]bool
}

func (k *restoreSink) EnsureUser(_ context.Context, a wpimport.Author) (int64, error) {
	if id, ok := k.logins[a.Login]; ok {
		return id, nil
	}
	if id, ok := k.users[a.ID]; ok && a.ID > 0 {
		return id, nil
	}
	return k.author, nil
}

func (k *restoreSink) EnsureTerm(ctx context.Context, taxonomy string, t wpimport.Term, parentID *int64) (int64, error) {
	if term, err := k.st.TermBySlug(ctx, taxonomy, t.Slug); err == nil {
		return term.ID, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if taxonomy != wpimport.TaxonomyCategory {
		parentID = nil
	}
	in := store.TermInput{Taxonomy: taxonomy, Name: t.Name, Slug: t.Slug, Description: t.Description, ParentID: parentID}
	for lang, tt := range t.I18n {
		if in.I18n == nil {
			in.I18n = map[string]store.TermText{}
		}
		in.I18n[lang] = store.TermText{Name: tt.Name, Description: tt.Description}
	}
	if t.ID > 0 && !k.terms[t.ID] {
		k.terms[t.ID] = true
		return t.ID, k.st.RestoreTerm(ctx, t.ID, in)
	}
	return k.st.CreateTerm(ctx, in)
}

func (k *restoreSink) PostExists(ctx context.Context, typ, lang, slug string) (bool, error) {
	return k.st.SlugExists(ctx, typ, cmp.Or(lang, k.mainLang), slug, 0)
}

func (k *restoreSink) CreatePost(ctx context.Context, p wpimport.Post) (int64, error) {
	in := store.PostInput{Type: p.Type, Status: p.Status, Title: p.Title, Slug: p.Slug, Lang: cmp.Or(p.Lang, k.mainLang),
		TranslationOf: p.TranslationOf, Excerpt: p.Excerpt, Content: p.Content, AuthorID: p.AuthorID, ParentID: p.ParentID,
		MenuOrder: p.MenuOrder, FeaturedMediaID: p.FeaturedMediaID, CommentStatus: p.CommentStatus,
		MetaDescription: p.MetaDescription, PublishedAt: p.PublishedAt, TermIDs: p.TermIDs, AssistantExclude: p.AssistantExclude}
	if p.OldID <= 0 || k.posts[p.OldID] {
		return k.st.CreatePost(ctx, in)
	}
	k.posts[p.OldID] = true
	err := k.st.RestorePost(ctx, p.OldID, in, p.ModifiedAt)
	if errors.Is(err, store.ErrTranslationExists) {
		in.TranslationOf = 0
		err = k.st.RestorePost(ctx, p.OldID, in, p.ModifiedAt)
	}
	return p.OldID, err
}

func (k *restoreSink) CreateComment(ctx context.Context, postID int64, c wpimport.Comment, parentID *int64) (int64, error) {
	ip, _ := netip.ParseAddr(c.AuthorIP)
	date := c.Date
	var user *int64
	if c.UserID > 0 {
		user = mapUser(k.users, &c.UserID)
	}
	nc := store.NewComment{PostID: postID, ParentID: parentID, UserID: user, AuthorName: c.Author, AuthorEmail: c.AuthorEmail,
		AuthorURL: c.AuthorURL, Content: c.Text, Status: c.Status, IP: ip, CreatedAt: &date}
	if c.ID <= 0 || k.comments[c.ID] {
		return k.st.CreateComment(ctx, nc)
	}
	k.comments[c.ID] = true
	return c.ID, k.st.RestoreComment(ctx, c.ID, nc)
}

func (k *restoreSink) StoreMedia(context.Context, string, io.Reader, string) (int64, string, error) {
	return 0, "", errors.New("restores do not download media")
}

// StoreAttachment maps an attachment to the media item restored from
// media.json, by the path of its address.
func (k *restoreSink) StoreAttachment(_ context.Context, _ *wpimport.Item, src string) (int64, string, error) {
	m := k.media[uploadPath(src)]
	if m == nil {
		return 0, "", errors.New("its file is not in the backup")
	}
	return m.ID, m.URL(), nil
}

// The importer passes no redirect to a restore (KeepAddresses, no rules):
// redirects come from redirects.json, with their own checks.
func (k *restoreSink) AddRedirect(context.Context, string, string, int) error {
	return wpimport.ErrRedirectInvalid
}

func (k *restoreSink) CheckRedirect(context.Context, string, string, int) error {
	return wpimport.ErrRedirectInvalid
}

// uploadPath is the path below the uploads of a media address: on the
// site (/uploads/…) or on WordPress (/wp-content/uploads/…).
func uploadPath(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return ""
	}
	p := u.Path
	for _, prefix := range []string{"/wp-content/uploads/", "/uploads/"} {
		if i := strings.Index(p, prefix); i >= 0 {
			return p[i+len(prefix):]
		}
	}
	return ""
}

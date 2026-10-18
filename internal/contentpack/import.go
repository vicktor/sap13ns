package contentpack

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/store"
)

// Options configure Import.
type Options struct {
	// AuthorID is the user that authors new items and signs revisions. Required.
	AuthorID int64
	// Site holds the settings and extra menu links from site.json. Nil keeps
	// the current settings (the main language is then read from them).
	Site *Site
	// Now is the publication time of items without a date (default time.Now()).
	Now time.Time
	// KeepRevisions is how many revisions UpdatePost keeps (default 10).
	KeepRevisions int
}

// Report describes what Import did.
type Report struct {
	Created, Updated, Unchanged int
	TermsCreated                int
	TermsTranslated             int
	SettingsChanged             bool
	MenusChanged                bool
	MenuItems                   int
	Lines                       []string // one per item, e.g. "created page en/features (id 3)"
	Warnings                    []string
}

func (r *Report) line(format string, args ...any) {
	r.Lines = append(r.Lines, fmt.Sprintf(format, args...))
}
func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *Report) String() string {
	var b strings.Builder
	if r.SettingsChanged {
		b.WriteString("settings: updated\n")
	} else {
		b.WriteString("settings: unchanged\n")
	}
	for _, l := range r.Lines {
		b.WriteString(l + "\n")
	}
	if r.MenusChanged {
		fmt.Fprintf(&b, "menus: rebuilt with %d items\n", r.MenuItems)
	} else {
		fmt.Fprintf(&b, "menus: unchanged (%d items)\n", r.MenuItems)
	}
	fmt.Fprintf(&b, "terms: %d created, %d translations set\n", r.TermsCreated, r.TermsTranslated)
	for _, w := range r.Warnings {
		b.WriteString("warning: " + w + "\n")
	}
	fmt.Fprintf(&b, "%d created, %d updated, %d unchanged\n", r.Created, r.Updated, r.Unchanged)
	return b.String()
}

// importer carries the state of one Import call.
type importer struct {
	st       *store.Store
	opts     Options
	rep      *Report
	mainLang string
	langs    []string // every site language, main first
	loc      *time.Location

	catIDs  map[string]int64  // category slug -> id
	termSet map[string]string // "<id>@<lang>" -> translated name set during this run
	// Results of the main-language items, keyed by type + key.
	mains map[string]*mainResult
}

type mainResult struct {
	id     int64
	catIDs []int64
	tagIDs []int64
}

// Import loads items into the store. It is idempotent: an item matches the
// existing row with the same type, language and slug (or, for a translation,
// the row of that language in its translation group) and is updated only when
// something changed. Settings come first, then the main-language items, then
// their translations, then the menus.
func Import(ctx context.Context, st *store.Store, items []Item, opts Options) (*Report, error) {
	if opts.AuthorID <= 0 {
		return nil, errors.New("contentpack: Options.AuthorID is required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.KeepRevisions <= 0 {
		opts.KeepRevisions = 10
	}
	im := &importer{st: st, opts: opts, rep: &Report{}, catIDs: map[string]int64{}, termSet: map[string]string{},
		mains: map[string]*mainResult{}}

	settings, err := im.applySettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	im.mainLang = settings.Language
	im.langs = append([]string{settings.Language}, settings.Languages...)
	im.loc = time.UTC
	if l, err := time.LoadLocation(settings.Timezone); err == nil {
		im.loc = l
	}
	if opts.Site != nil {
		im.rep.Warnings = append(im.rep.Warnings, opts.Site.Warnings...)
	}

	// Resolve dates up front so that a bad one aborts before any write.
	dates := make([]*time.Time, len(items))
	for i := range items {
		it := &items[i]
		if it.Date == "" {
			continue
		}
		t, err := ParseDate(it.Date, im.loc)
		if err != nil {
			return im.rep, fmt.Errorf("%s:%d: %w", it.File, it.DateLine, err)
		}
		dates[i] = &t
		if t.After(opts.Now) && it.Status == store.StatusPublished {
			im.rep.warn("%s: date %s is in the future; the item will be scheduled", it.File, it.Date)
		}
	}
	for i := range items {
		if !slices.Contains(im.langs, items[i].Lang) {
			im.rep.warn("%s: language %q is not one of the site languages %v; it will not be reachable", items[i].File, items[i].Lang, im.langs)
		}
	}

	// Main language first so that translations can point to it.
	for pass := 0; pass < 2; pass++ {
		for i := range items {
			it := &items[i]
			if (it.Lang == im.mainLang) != (pass == 0) {
				continue
			}
			if err := im.importItem(ctx, it, dates[i]); err != nil {
				return im.rep, fmt.Errorf("%s: %w", it.File, err)
			}
		}
	}

	if err := im.applyMenus(ctx, items); err != nil {
		return im.rep, fmt.Errorf("menus: %w", err)
	}
	return im.rep, nil
}

func (im *importer) applySettings(ctx context.Context) (store.Settings, error) {
	cur, err := im.st.LoadSettings(ctx)
	if err != nil {
		return cur, err
	}
	site := im.opts.Site
	if site == nil {
		return cur, nil
	}
	next := cur
	next.SiteTitle = site.Title
	next.Tagline = site.Tagline
	next.FooterText = site.FooterText
	next.Language = site.Language
	next.Languages = append([]string{}, site.Languages...)
	next.SiteText = map[string]store.SiteText{}
	for k, v := range cur.SiteText {
		next.SiteText[k] = v
	}
	for k, v := range site.SiteText {
		next.SiteText[k] = v
	}
	if site.ActiveTheme != "" {
		next.ActiveTheme = site.ActiveTheme
	}
	if site.Timezone != "" {
		next.Timezone = site.Timezone
	}
	if site.PostsPerPage > 0 {
		next.PostsPerPage = site.PostsPerPage
	}
	next.FrontPage = "posts"
	if reflect.DeepEqual(normSettings(cur), normSettings(next)) {
		return cur, nil
	}
	if err := im.st.SaveSettings(ctx, next); err != nil {
		return cur, err
	}
	im.rep.SettingsChanged = true
	return next, nil
}

// normSettings treats empty and nil slices and maps alike for comparison.
func normSettings(s store.Settings) store.Settings {
	if len(s.Languages) == 0 {
		s.Languages = nil
	}
	if len(s.SiteText) == 0 {
		s.SiteText = nil
	}
	return s
}

func (im *importer) importItem(ctx context.Context, it *Item, date *time.Time) error {
	mk := it.Type + "\x00" + it.Key
	var main *mainResult
	var catIDs, tagIDs []int64
	var err error
	if it.Lang == im.mainLang {
		if catIDs, err = im.categories(ctx, it.Categories); err != nil {
			return err
		}
		if tagIDs, err = im.tags(ctx, it.Tags); err != nil {
			return err
		}
	} else if main = im.mains[mk]; main != nil {
		if catIDs, err = im.translateTerms(ctx, it, "category", main.catIDs, it.Categories); err != nil {
			return err
		}
		if tagIDs, err = im.translateTerms(ctx, it, "tag", main.tagIDs, it.Tags); err != nil {
			return err
		}
	} else {
		im.rep.warn("%s: no %s with key %q in the main language %q; imported without a translation link", it.File, it.Type, it.Key, im.mainLang)
		if catIDs, err = im.categories(ctx, it.Categories); err != nil {
			return err
		}
		if tagIDs, err = im.tags(ctx, it.Tags); err != nil {
			return err
		}
	}
	termIDs := dedupe(append(append([]int64{}, catIDs...), tagIDs...))

	existing, err := im.st.PostBySlug(ctx, it.Type, it.Lang, it.Slug)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if existing == nil && main != nil {
		// The slug may have changed: reuse the group's row in this language.
		if existing, err = im.sibling(ctx, main.id, it.Type, it.Lang); err != nil {
			return err
		}
	}

	in := store.PostInput{Type: it.Type, Status: it.Status, Title: it.Title, Slug: it.Slug, Lang: it.Lang,
		Excerpt: it.Excerpt, Content: it.Body, AuthorID: im.opts.AuthorID, MenuOrder: it.Order,
		CommentStatus: it.Comments, MetaDescription: it.MetaDescription, TermIDs: termIDs}
	label := fmt.Sprintf("%s %s/%s", it.Type, it.Lang, it.Slug)

	var id int64
	if existing == nil {
		in.PublishedAt = date
		if in.PublishedAt == nil && it.Status == store.StatusPublished {
			now := im.opts.Now
			in.PublishedAt = &now
		}
		if main != nil {
			in.TranslationOf = main.id
		}
		if id, err = im.st.CreatePost(ctx, in); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("create %s: the slug is taken, perhaps by a post in the trash: %w", label, err)
			}
			return fmt.Errorf("create %s: %w", label, err)
		}
		im.rep.Created++
		im.rep.line("created   %s (id %d)", label, id)
	} else {
		id = existing.ID
		if existing.AuthorID != nil {
			in.AuthorID = *existing.AuthorID
		}
		in.ParentID = existing.ParentID
		in.FeaturedMediaID = existing.FeaturedMediaID
		in.AssistantExclude = existing.AssistantExclude // an editor's choice, not part of the pack
		in.PublishedAt = date
		if in.PublishedAt == nil {
			in.PublishedAt = existing.PublishedAt
		}
		if in.PublishedAt == nil && it.Status == store.StatusPublished {
			now := im.opts.Now
			in.PublishedAt = &now
		}
		if same(existing, &in) {
			im.rep.Unchanged++
			im.rep.line("unchanged %s (id %d)", label, id)
		} else {
			if err := im.st.UpdatePost(ctx, id, in, im.opts.AuthorID, im.opts.KeepRevisions); err != nil {
				return fmt.Errorf("update %s: %w", label, err)
			}
			im.rep.Updated++
			im.rep.line("updated   %s (id %d)", label, id)
		}
		if main != nil {
			if err := im.ensureLinked(ctx, existing, main.id); err != nil {
				return err
			}
		}
	}

	if it.Lang == im.mainLang {
		im.mains[mk] = &mainResult{id: id, catIDs: catIDs, tagIDs: tagIDs}
	}
	return nil
}

// same reports whether saving in would leave the post as it is.
func same(p *store.Post, in *store.PostInput) bool {
	if p.Title != in.Title || p.Slug != in.Slug || p.Lang != in.Lang || p.Status != in.Status || p.Excerpt != in.Excerpt ||
		p.Content != in.Content || p.MenuOrder != in.MenuOrder || p.CommentStatus != in.CommentStatus ||
		p.MetaDescription != in.MetaDescription {
		return false
	}
	if (p.AuthorID == nil) != (in.AuthorID == 0) || (p.AuthorID != nil && *p.AuthorID != in.AuthorID) {
		return false
	}
	if (p.PublishedAt == nil) != (in.PublishedAt == nil) || (p.PublishedAt != nil && !p.PublishedAt.Equal(*in.PublishedAt)) {
		return false
	}
	have := make([]int64, 0, len(p.Terms))
	for _, t := range p.Terms {
		have = append(have, t.ID)
	}
	want := append([]int64{}, in.TermIDs...)
	slices.Sort(have)
	slices.Sort(want)
	return slices.Equal(have, want)
}

// sibling returns the row of a translation group in lang, if any.
func (im *importer) sibling(ctx context.Context, mainID int64, typ, lang string) (*store.Post, error) {
	mp, err := im.st.PostByID(ctx, mainID)
	if err != nil {
		return nil, err
	}
	trs, err := im.st.Translations(ctx, mp)
	if err != nil {
		return nil, err
	}
	for _, t := range trs {
		if t.ID != mainID && t.Lang == lang && t.Type == typ {
			return im.st.PostByID(ctx, t.ID)
		}
	}
	return nil, nil
}

// ensureLinked puts an existing translation in the group of its main-language
// post. The store only links translations when a post is created
// (PostInput.TranslationOf), so this mirrors CreatePost's grouping with a
// direct UPDATE of posts.tgroup.
func (im *importer) ensureLinked(ctx context.Context, p *store.Post, mainID int64) error {
	trs, err := im.st.Translations(ctx, p)
	if err != nil {
		return err
	}
	for _, t := range trs {
		if t.ID == mainID {
			return nil
		}
	}
	var group int64
	if err := im.st.DB.QueryRow(ctx, `SELECT coalesce(tgroup, id) FROM posts WHERE id = $1`, mainID).Scan(&group); err != nil {
		return err
	}
	var taken bool
	if err := im.st.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE (tgroup = $1 OR id = $1) AND lang = $2 AND id <> $3)`,
		group, p.Lang, p.ID).Scan(&taken); err != nil {
		return err
	}
	if taken {
		im.rep.warn("%s/%s (id %d): cannot link it as a translation of id %d, whose group already has a %q version",
			p.Lang, p.Slug, p.ID, mainID, p.Lang)
		return nil
	}
	if err := im.st.UnlinkTranslation(ctx, p.ID); err != nil {
		return err
	}
	err = im.st.InTx(ctx, func(tx pgx.Tx) error {
		var g int64
		if err := tx.QueryRow(ctx, `UPDATE posts SET tgroup = coalesce(tgroup, id) WHERE id = $1 RETURNING tgroup`, mainID).Scan(&g); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE posts SET tgroup = $1 WHERE id = $2`, g, p.ID)
		return err
	})
	if err != nil {
		return err
	}
	im.rep.line("linked    %s/%s (id %d) as a translation of id %d", p.Lang, p.Slug, p.ID, mainID)
	return nil
}

// categories returns the ids of categories by display name, creating them.
func (im *importer) categories(ctx context.Context, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		slug := Slugify(name)
		if id, ok := im.catIDs[slug]; ok {
			ids = append(ids, id)
			continue
		}
		t, err := im.st.TermBySlug(ctx, "category", slug)
		var id int64
		switch {
		case err == nil:
			id = t.ID
		case errors.Is(err, store.ErrNotFound):
			if id, err = im.st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: name, Slug: slug}); err != nil {
				return nil, fmt.Errorf("category %q: %w", name, err)
			}
			im.rep.TermsCreated++
		default:
			return nil, err
		}
		im.catIDs[slug] = id
		ids = append(ids, id)
	}
	return dedupe(ids), nil
}

// tags returns the ids of tags by display name, creating them.
func (im *importer) tags(ctx context.Context, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var ns, slugs []string
	for _, n := range names {
		s := Slugify(n)
		if slices.Contains(slugs, s) {
			continue
		}
		if _, err := im.st.TermBySlug(ctx, "tag", s); errors.Is(err, store.ErrNotFound) {
			im.rep.TermsCreated++
		}
		ns, slugs = append(ns, n), append(slugs, s)
	}
	return im.st.EnsureTags(ctx, ns, slugs)
}

// translateTerms gives a translation the terms of its main-language item,
// pairing names by position and storing each translated name in the term's
// I18n. Names beyond the main item's list become terms of their own.
func (im *importer) translateTerms(ctx context.Context, it *Item, taxonomy string, mainIDs []int64, names []string) ([]int64, error) {
	if len(names) != len(mainIDs) {
		im.rep.warn("%s: %d %s names but the main-language item has %d; pairing them by position",
			it.File, len(names), taxonomy, len(mainIDs))
	}
	ids := append([]int64{}, mainIDs...)
	for i, name := range names {
		if i >= len(mainIDs) {
			var extra []int64
			var err error
			if taxonomy == "category" {
				extra, err = im.categories(ctx, []string{name})
			} else {
				extra, err = im.tags(ctx, []string{name})
			}
			if err != nil {
				return nil, err
			}
			ids = append(ids, extra...)
			continue
		}
		if err := im.translateTerm(ctx, it, mainIDs[i], name); err != nil {
			return nil, err
		}
	}
	return dedupe(ids), nil
}

func (im *importer) translateTerm(ctx context.Context, it *Item, id int64, name string) error {
	key := fmt.Sprintf("%d@%s", id, it.Lang)
	if prev, ok := im.termSet[key]; ok {
		if prev != name {
			im.rep.warn("%s: term %d is called %q in %q elsewhere in the pack; keeping %q", it.File, id, prev, it.Lang, prev)
		}
		return nil
	}
	im.termSet[key] = name
	t, err := im.st.TermByID(ctx, id)
	if err != nil {
		return err
	}
	cur := t.I18n[it.Lang]
	if cur.Name == name || (cur.Name == "" && name == t.Name) {
		return nil
	}
	i18n := map[string]store.TermText{}
	for k, v := range t.I18n {
		i18n[k] = v
	}
	i18n[it.Lang] = store.TermText{Name: name, Description: cur.Description}
	if err := im.st.UpdateTerm(ctx, id, store.TermInput{Taxonomy: t.Taxonomy, Name: t.Name, Slug: t.Slug,
		Description: t.Description, ParentID: t.ParentID, I18n: i18n}); err != nil {
		return fmt.Errorf("translate %s %q: %w", t.Taxonomy, t.Name, err)
	}
	im.rep.TermsTranslated++
	return nil
}

type menuEntry struct {
	label, url string
	order      int
	seq        int
}

// applyMenus replaces every menu with the entries declared by the pack. When
// the pack declares no menu entries at all, existing menus are left alone.
func (im *importer) applyMenus(ctx context.Context, items []Item) error {
	want := map[string][]menuEntry{} // store.MenuKey -> entries
	seq := 0
	add := func(location, lang string, e menuEntry) {
		if lang == im.mainLang {
			lang = ""
		}
		e.seq = seq
		seq++
		k := store.MenuKey(location, lang)
		want[k] = append(want[k], e)
	}
	for i := range items {
		it := &items[i]
		if it.Status != store.StatusPublished {
			continue
		}
		for _, loc := range it.Menus {
			add(loc, it.Lang, menuEntry{label: it.Label(loc), url: it.Path(im.mainLang), order: it.Order})
		}
	}
	if im.opts.Site != nil {
		for _, m := range im.opts.Site.MenuLinks {
			if !slices.Contains(im.langs, m.Lang) {
				im.rep.warn("menu_links: %q (%s) is for %q, which is not a site language", m.Label, m.URL, m.Lang)
			}
			add(m.Menu, m.Lang, menuEntry{label: m.Label, url: m.URL, order: m.Order})
		}
	}
	for _, list := range want {
		sort.SliceStable(list, func(a, b int) bool {
			if list[a].order != list[b].order {
				return list[a].order < list[b].order
			}
			if list[a].label != list[b].label {
				return list[a].label < list[b].label
			}
			return list[a].seq < list[b].seq
		})
	}

	have, err := im.st.AllMenus(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, l := range want {
		total += len(l)
	}
	im.rep.MenuItems = total
	if total == 0 {
		n := 0
		for _, l := range have {
			n += len(l)
		}
		im.rep.MenuItems = n
		im.rep.warn("the pack declares no menu entries; existing menus were left as they are")
		return nil
	}
	if menusEqual(have, want) {
		return nil
	}
	for _, list := range have {
		for _, it := range list {
			if err := im.st.DeleteMenuItem(ctx, it.ID); err != nil {
				return err
			}
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		loc, lang, _ := strings.Cut(k, "@")
		for _, e := range want[k] {
			if err := im.st.AddMenuItem(ctx, loc, lang, e.label, e.url); err != nil {
				return fmt.Errorf("%s: %q: %w", k, e.label, err)
			}
		}
	}
	im.rep.MenusChanged = true
	return nil
}

func menusEqual(have map[string][]store.MenuItem, want map[string][]menuEntry) bool {
	if len(have) != len(want) {
		return false
	}
	for k, w := range want {
		h := have[k]
		if len(h) != len(w) {
			return false
		}
		for i := range w {
			if h[i].Label != w[i].label || h[i].URL != w[i].url {
				return false
			}
		}
	}
	return true
}

func dedupe(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

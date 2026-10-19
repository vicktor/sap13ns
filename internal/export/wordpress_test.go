package export

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/internal/wpimport"
)

// TestWordPressShape checks what the WordPress Importer needs beyond the
// posts: menus as nav_menu terms and nav_menu_item posts, featured images
// attached to their post, replies threaded to an exported ancestor,
// trashed posts on request, and addresses on a new WordPress site.
func TestWordPressShape(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	s := seed(t, st, "gamma")
	parent, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Parent", Slug: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Child", Slug: "child", ParentID: &parent}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "tag", Name: "Go", Slug: "go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePost(ctx, store.PostInput{Type: "page", Status: "published", Title: "About", Slug: "about", Lang: "es",
		Content: "About us", AuthorID: s.admin, CommentStatus: "closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "trash", Title: "Binned gamma", Slug: "binned",
		Lang: "es", Content: "Binned", AuthorID: s.admin, CommentStatus: "open"}); err != nil {
		t.Fatal(err)
	}
	// An image with resized copies, inserted in a post as the editor does.
	mid, err := st.CreateMedia(ctx, &store.Media{UploaderID: &s.admin, Path: "2026/10/photo.jpg", OriginalName: "Photo.jpg",
		MIME: "image/jpeg", Size: 10, Variants: map[string]store.MediaVariant{"medium": {Path: "2026/10/photo-medium.jpg", Width: 1024}},
		SHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	if _, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: "Photo post", Slug: "photo",
		Lang: "es", Content: "![p](/uploads/2026/10/photo-medium.jpg)", AuthorID: s.admin, CommentStatus: "open",
		PublishedAt: &pub, FeaturedMediaID: &mid}); err != nil {
		t.Fatal(err)
	}
	// approved ← pending ← approved: the last reply hangs from the first.
	ip := netip.MustParseAddr("192.0.2.8")
	c1, err := st.CreateComment(ctx, store.NewComment{PostID: s.posts[0], AuthorName: "A", Content: "root", Status: "approved", IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := st.CreateComment(ctx, store.NewComment{PostID: s.posts[0], ParentID: &c1, AuthorName: "B", Content: "held", Status: "pending", IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	c3, err := st.CreateComment(ctx, store.NewComment{PostID: s.posts[0], ParentID: &c2, AuthorName: "C", Content: "reply", Status: "approved", IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []struct{ loc, lang, label, url string }{
		{"primary", "", "About", "/about"},
		{"primary", "", "Parent", "/category/parent"},
		{"primary", "", "Tag", "https://example.com/tag/go"},
		{"primary", "", "Elsewhere", "https://other.example.org/x"},
		{"primary", "", "Home", "/"},
		{"primary", "en", "Hello", "/en/blog/gamma-hello"},
	} {
		if err := st.AddMenuItem(ctx, it.loc, it.lang, it.label, it.url); err != nil {
			t.Fatal(err)
		}
	}

	out, rep := wxr(t, st, siteOpts(s))
	if rep.Menus != 2 || rep.MenuItems != 6 {
		t.Errorf("menus %d, items %d", rep.Menus, rep.MenuItems)
	}
	exp, err := wpimport.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if exp.Ignored["nav_menu_item"] != 6 {
		t.Errorf("our importer should skip the menu items, ignored %v", exp.Ignored)
	}
	var (
		photoID, attParent int64
		threaded           bool
	)
	for _, it := range exp.Items {
		switch {
		case it.Type == "post" && it.Title == "Photo post":
			photoID = it.ID
			if !strings.Contains(it.Content, `src="https://example.com/uploads/2026/10/photo.jpg"`) {
				t.Errorf("the medium copy is not swapped for the original: %s", it.Content)
			}
			if it.Markdown != "![p](/uploads/2026/10/photo-medium.jpg)" {
				t.Errorf("the Markdown source changed: %q", it.Markdown)
			}
		case it.Type == "attachment" && it.AttachmentURL == "https://example.com/uploads/2026/10/photo.jpg":
			attParent = it.ParentID
		case it.Title == "Binned gamma":
			t.Error("trashed post exported without asking")
		}
		for _, c := range it.Comments {
			if c.ID == c3 && c.ParentID == c1 {
				threaded = true
			}
			if c.ID == c2 {
				t.Error("pending comment exported")
			}
		}
	}
	if photoID == 0 || attParent != photoID {
		t.Errorf("attachment parent %d, want the post featuring it (%d)", attParent, photoID)
	}
	if !threaded {
		t.Error("the reply to a held comment is not attached to its exported ancestor")
	}
	meta := func(k, v string) string {
		return `<wp:meta_key><![CDATA[` + k + `]]></wp:meta_key><wp:meta_value><![CDATA[` + v + `]]></wp:meta_value>`
	}
	for _, want := range []string{
		`<wp:term_taxonomy><![CDATA[nav_menu]]></wp:term_taxonomy><wp:term_slug><![CDATA[primary]]></wp:term_slug>`,
		`<wp:term_slug><![CDATA[primary-en]]></wp:term_slug>`,
		`<category domain="nav_menu" nicename="primary-en">`,
		`<wp:category_parent><![CDATA[parent]]></wp:category_parent>`,
		meta("_menu_item_object", "page"),
		meta("_menu_item_object", "category"),
		meta("_menu_item_object", "post_tag"),
		meta("_menu_item_object", "post"),
		meta("_menu_item_url", "https://other.example.org/x"),
		meta("_menu_item_url", "https://example.com/"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %s", want)
		}
	}

	// Trash on request, as WordPress keeps it.
	o := siteOpts(s)
	o.Statuses = []string{store.StatusPublished, store.StatusTrash}
	out, _ = wxr(t, st, o)
	if !strings.Contains(out, "Binned gamma") || !strings.Contains(out, "<wp:status><![CDATA[trash]]></wp:status>") ||
		!strings.Contains(out, "_wp_trash_meta_status") {
		t.Error("trashed post not exported as trash")
	}

	// Moving to WordPress: files and links at the new site.
	o = siteOpts(s)
	o.WPBase = "https://new.example/"
	out, _ = wxr(t, st, o)
	for _, want := range []string{
		`<wp:attachment_url><![CDATA[https://new.example/wp-content/uploads/sap13ns-import/2026/10/photo.jpg]]></wp:attachment_url>`,
		`src="https://new.example/wp-content/uploads/sap13ns-import/2026/10/photo.jpg"`,
		`src="https://new.example/wp-content/uploads/sap13ns-import/2026/10/gamma.png"`,
		// Where WordPress keeps it: the importer copies it there.
		`<wp:meta_key><![CDATA[_wp_attached_file]]></wp:meta_key><wp:meta_value><![CDATA[2026/10/photo.jpg]]></wp:meta_value>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export for WordPress lacks %s", want)
		}
	}
	if strings.Contains(out, "https://example.com/uploads/") {
		t.Error("export for WordPress still points at the old uploads")
	}
	// Menu links to the site's own pages go to the new site too.
	for _, want := range []string{meta("_menu_item_url", "https://new.example/"), meta("_menu_item_url", "https://other.example.org/x")} {
		if !strings.Contains(out, want) {
			t.Errorf("export for WordPress lacks %s", want)
		}
	}
	if strings.Contains(out, meta("_menu_item_url", "https://example.com/")) {
		t.Error("a menu link of the export for WordPress points at the old site")
	}

	o = siteOpts(s)
	o.NoMenus = true
	if out, rep = wxr(t, st, o); rep.MenuItems != 0 || strings.Contains(out, "nav_menu") {
		t.Error("menus exported with NoMenus")
	}
}

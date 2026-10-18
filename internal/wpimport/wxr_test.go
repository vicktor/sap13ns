package wpimport

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T) *Export {
	t.Helper()
	f, err := os.Open("testdata/export.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	exp, err := Parse(LimitReader(f, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func findItem(t *testing.T, exp *Export, id int64) *Item {
	t.Helper()
	for i := range exp.Items {
		if exp.Items[i].ID == id {
			return &exp.Items[i]
		}
	}
	t.Fatalf("item %d not found", id)
	return nil
}

func TestParseFixture(t *testing.T) {
	exp := loadFixture(t)

	site := exp.Site
	if site.Title != "Café Ñandú" || site.Link != "https://blog.example.com" || site.Language != "es-ES" ||
		site.BaseSiteURL != "https://blog.example.com" || site.BaseBlogURL != "https://blog.example.com" || site.WXRVersion != "1.2" {
		t.Errorf("site = %+v", site)
	}
	if site.Description != "Un blog de pruebas – con acentos" {
		t.Errorf("description = %q", site.Description)
	}

	if len(exp.Authors) != 2 {
		t.Fatalf("authors = %+v", exp.Authors)
	}
	if a := exp.Authors[1]; a.ID != 2 || a.Login != "maria" || a.Email != "maria@example.com" || a.DisplayName != "María Pérez" ||
		a.FirstName != "María" || a.LastName != "Pérez" {
		t.Errorf("author = %+v", a)
	}

	if len(exp.Categories) != 4 {
		t.Fatalf("categories = %+v", exp.Categories)
	}
	if c := exp.Categories[0]; c.Slug != "espa%c3%b1a" || c.Name != "España" || c.Parent != "europa" || c.ID != 7 {
		t.Errorf("category = %+v", c)
	}
	if c := exp.Categories[2]; c.Description != "Crónicas de viaje" || c.Parent != "" {
		t.Errorf("category = %+v", c)
	}
	if exp.Categories[3].Name != "Noticias &amp; avisos" {
		t.Errorf("CDATA is not entity-decoded by the parser (the importer does it), got %q", exp.Categories[3].Name)
	}
	if len(exp.Tags) != 2 || exp.Tags[1].Slug != "caf%c3%a9" || exp.Tags[1].Description != "Todo sobre el café" {
		t.Errorf("tags = %+v", exp.Tags)
	}

	types := map[string]int{}
	for _, it := range exp.Items {
		types[it.Type]++
	}
	if types["attachment"] != 3 || types["page"] != 4 || types["post"] != 8 || len(types) != 3 {
		t.Errorf("item types = %v", types)
	}
	for _, typ := range []string{"nav_menu_item", "revision", "wp_block", "custom_css", "product"} {
		if exp.Ignored[typ] != 1 {
			t.Errorf("ignored = %v, want one %s", exp.Ignored, typ)
		}
	}

	p := findItem(t, exp, 20)
	if p.Title != "¡Hola mundo!" || p.Link != "https://blog.example.com/2020/01/15/hola-mundo/" || p.GUID != "https://blog.example.com/?p=20" ||
		p.Creator != "admin" || p.Slug != "hola-mundo" || p.Status != "publish" || p.Type != "post" || !p.Sticky ||
		p.CommentStatus != "open" || p.ThumbnailID != 10 || p.Protected {
		t.Errorf("post 20 = %+v", p)
	}
	if want := time.Date(2020, 1, 15, 10, 30, 0, 0, time.UTC); !p.Date.Equal(want) {
		t.Errorf("date = %v, want GMT %v", p.Date, want)
	}
	if want := time.Date(2020, 1, 20, 11, 0, 0, 0, time.UTC); !p.Modified.Equal(want) {
		t.Errorf("modified = %v", p.Modified)
	}
	if !strings.HasPrefix(p.Content, "Bienvenido a <strong>") || !strings.Contains(p.Content, "[/caption]") {
		t.Errorf("content = %q", p.Content)
	}
	wantTerms := []ItemTerm{
		{Domain: "category", Slug: "noticias", Name: "Noticias &amp; avisos"},
		{Domain: "post_tag", Slug: "go", Name: "Go"},
		{Domain: "post_format", Slug: "post-format-aside", Name: "Aside"},
	}
	if len(p.Terms) != len(wantTerms) {
		t.Fatalf("terms = %+v", p.Terms)
	}
	for i, tm := range wantTerms {
		if p.Terms[i] != tm {
			t.Errorf("term %d = %+v, want %+v", i, p.Terms[i], tm)
		}
	}
	if len(p.Comments) != 7 {
		t.Fatalf("comments = %d", len(p.Comments))
	}
	c := p.Comments[0]
	if c.ID != 3 || c.Author != "Luis" || c.AuthorEmail != "luis@example.net" || c.AuthorIP != "203.0.113.7" ||
		c.Approved != "0" || c.ParentID != 2 || c.Type != "comment" || c.Content != "Una pregunta &amp; una duda" {
		t.Errorf("comment = %+v", c)
	}
	if !c.Date.Equal(time.Date(2020, 1, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("comment date = %v", c.Date)
	}
	if p.Comments[1].UserID != 1 || p.Comments[1].AuthorURL != "https://blog.example.com" {
		t.Errorf("comment = %+v", p.Comments[1])
	}
	if p.Comments[4].Type != "pingback" || p.Comments[3].Approved != "spam" {
		t.Errorf("comments = %+v", p.Comments)
	}

	if a := findItem(t, exp, 10); a.AttachmentURL != "https://blog.example.com/wp-content/uploads/2020/01/photo-scaled.jpg" ||
		a.AttachmentAlt != "Una foto" || a.ParentID != 20 || a.Status != "inherit" {
		t.Errorf("attachment = %+v", a)
	}
	if pg := findItem(t, exp, 31); pg.PageTemplate != "templates/full-width.php" || pg.ParentID != 30 || pg.MenuOrder != 2 {
		t.Errorf("page = %+v", pg)
	}
	draft := findItem(t, exp, 23)
	if want := time.Date(2024, 2, 2, 12, 0, 0, 0, time.UTC); !draft.Date.Equal(want) || draft.Slug != "" {
		t.Errorf("draft without GMT date should fall back to post_date: %+v", draft)
	}
	if !findItem(t, exp, 26).Protected {
		t.Error("password-protected post not flagged")
	}
	if ex := findItem(t, exp, 22).Excerpt; ex != "Un adelanto <em>explícito</em>." {
		t.Errorf("excerpt = %q", ex)
	}
}

func TestParseRejectsXMLBombs(t *testing.T) {
	billionLaughs := `<?xml version="1.0"?>
<!DOCTYPE lolz [
 <!ENTITY lol "lol">
 <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<rss><channel><title>&lol3;</title></channel></rss>`
	xxe := `<?xml version="1.0"?>
<!DOCTYPE rss [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<rss><channel><title>&xxe;</title></channel></rss>`
	lateDoctype := `<rss><channel><!DOCTYPE x><title>x</title></channel></rss>`
	for name, doc := range map[string]string{"billion laughs": billionLaughs, "xxe": xxe, "late doctype": lateDoctype} {
		if _, err := Parse(strings.NewReader(doc)); !errors.Is(err, ErrDoctype) {
			t.Errorf("%s: err = %v, want ErrDoctype", name, err)
		}
	}
	// An entity that was never declared is a plain syntax error.
	if _, err := Parse(strings.NewReader(`<rss><channel><title>&xxe;</title></channel></rss>`)); err == nil {
		t.Error("undeclared entity accepted")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(strings.NewReader(`<html><body>hi</body></html>`)); !errors.Is(err, ErrNotWXR) {
		t.Errorf("html: %v", err)
	}
	if _, err := Parse(strings.NewReader(`<rss version="2.0"></rss>`)); !errors.Is(err, ErrNotWXR) {
		t.Errorf("empty rss: %v", err)
	}
	if _, err := Parse(strings.NewReader(`<rss><channel><item><title>x</title>`)); err == nil {
		t.Error("truncated document accepted")
	}
	deep := "<rss><channel><item>" + strings.Repeat("<x>", 100) + strings.Repeat("</x>", 100) + "</item></channel></rss>"
	if _, err := Parse(strings.NewReader(deep)); !errors.Is(err, ErrTooDeep) {
		t.Errorf("deep: %v", err)
	}

	data, err := os.ReadFile("testdata/export.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(LimitReader(bytes.NewReader(data), 4096)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("size cap: err = %v, want ErrTooLarge", err)
	}
}

func TestParseLenientInput(t *testing.T) {
	// Control characters pasted into a post, HTML entities outside CDATA and
	// a legacy charset must not abort the import.
	doc := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<rss xmlns:wp=\"http://wordpress.org/export/1.1/\" xmlns:content=\"http://purl.org/rss/1.0/modules/content/\">" +
		"<channel><title>T&eacute;st&nbsp;site</title><item><title>A\x0bB</title><wp:post_type>post</wp:post_type>" +
		"<content:encoded><![CDATA[x\x01y]]></content:encoded><category><![CDATA[Old style]]></category></item></channel></rss>"
	exp, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if exp.Site.Title != "Tést\u00a0site" || exp.Items[0].Title != "AB" || exp.Items[0].Content != "xy" {
		t.Errorf("got %+v / %+v", exp.Site, exp.Items[0])
	}
	if tm := exp.Items[0].Terms; len(tm) != 1 || tm[0].Domain != "category" || tm[0].Slug != "Old style" {
		t.Errorf("WXR 1.0 category = %+v", tm)
	}

	latin1 := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><channel><title>Caf\xe9</title></channel></rss>")
	exp, err = Parse(bytes.NewReader(latin1))
	if err != nil {
		t.Fatal(err)
	}
	if exp.Site.Title != "Café" {
		t.Errorf("latin1 title = %q", exp.Site.Title)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"hola-mundo":              "hola-mundo",
		"caf%c3%a9-con-le%c3%b1a": "cafe-con-lena",
		"Árbol Ñandú":             "arbol-nandu",
		"Straße & Œuvre":          "strasse-oeuvre",
		"don't stop":              "dont-stop",
		"  --Hello__World--  ":    "hello-world",
		"%e6%97%a5%e6%9c%ac":      "", // Japanese: no transliteration
		"100%":                    "100",
		"%zz":                     "zz",
		"":                        "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Slugify(strings.Repeat("abc-", 100)); len(got) > maxSlugLen || strings.HasSuffix(got, "-") {
		t.Errorf("long slug = %q", got)
	}
}

func FuzzParse(f *testing.F) {
	if data, err := os.ReadFile("testdata/export.xml"); err == nil {
		f.Add(data)
	}
	f.Add([]byte(`<rss><channel><item><wp:post_type>post</wp:post_type></item></channel></rss>`))
	f.Add([]byte(`<!DOCTYPE x [<!ENTITY a "b">]><rss/>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		exp, err := Parse(LimitReader(bytes.NewReader(data), 1<<20))
		if err == nil && exp == nil {
			t.Fatal("nil export without error")
		}
	})
}

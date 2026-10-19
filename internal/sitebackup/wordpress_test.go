package sitebackup

// TestWordPressImport imports a backup into a real WordPress, in Docker:
// SAP13NS_WP_TEST=1 go test ./internal/sitebackup -run TestWordPressImport
// -timeout 30m. It needs the images wordpress, mariadb:11 and
// wordpress:cli, and Internet access from the containers (wp-cli installs
// the WordPress Importer). The containers share a network of their own and
// publish no port; everything it starts is named sap13ns-wptest-<random>,
// and removed at the end, whatever the outcome.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/export"
	"sap13ns/internal/testutil"
)

const (
	wpImage  = "wordpress:latest"
	dbImage  = "mariadb:11"
	cliImage = "wordpress:cli"
	// The old site, as WordPress sees it in the first mode: a static
	// server of the uploads in the Docker network. WordPress fetches only
	// from ports 80, 443 and 8080.
	originURL = "http://origin.test:8080"
	wpURL     = "http://wp.test"
)

// wpStack is a WordPress with its database, and the old site.
type wpStack struct {
	t                              *testing.T
	name, net, db, wp, vol, origin string
	dbPass                         string
	containers, volumes, networks  []string
}

func (s *wpStack) docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (s *wpStack) must(args ...string) string {
	s.t.Helper()
	out, err := s.docker(args...)
	if err != nil {
		s.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (s *wpStack) dbEnv() []string {
	return []string{"-e", "WORDPRESS_DB_HOST=" + s.db, "-e", "WORDPRESS_DB_USER=wordpress",
		"-e", "WORDPRESS_DB_PASSWORD=" + s.dbPass, "-e", "WORDPRESS_DB_NAME=wordpress"}
}

// cli runs wp-cli on the WordPress.
func (s *wpStack) cli(args ...string) (string, error) {
	// As the www-data of the wordpress image, which owns its files (that of
	// the Alpine-based wordpress:cli is 82).
	full := append([]string{"run", "--rm", "--user", "33:33", "--network", s.net, "-v", s.vol + ":/var/www/html"}, s.dbEnv()...)
	full = append(full, cliImage, "wp")
	return s.docker(append(full, args...)...)
}

func (s *wpStack) mustWP(args ...string) string {
	s.t.Helper()
	out, err := s.cli(args...)
	if err != nil {
		s.t.Fatalf("wp %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (s *wpStack) json(v any, args ...string) {
	s.t.Helper()
	out := s.mustWP(append(args, "--format=json")...)
	// wp-cli may print PHP notices before the JSON.
	if i := strings.IndexAny(out, "[{"); i > 0 {
		out = out[i:]
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		s.t.Fatalf("wp %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (s *wpStack) cleanup() {
	for _, c := range s.containers {
		s.docker("rm", "-f", "-v", c)
	}
	for _, v := range s.volumes {
		s.docker("volume", "rm", "-f", v)
	}
	for _, n := range s.networks {
		s.docker("network", "rm", n)
	}
}

// startWordPress starts a WordPress at wpURL, installed, with the
// WordPress Importer.
func startWordPress(t *testing.T) *wpStack {
	t.Helper()
	b := make([]byte, 5)
	rand.Read(b)
	name := "sap13ns-wptest-" + hex.EncodeToString(b)
	s := &wpStack{t: t, name: name, net: name, db: name + "-db", wp: name + "-wp", vol: name + "-html", origin: name + "-origin"}
	pw := make([]byte, 12)
	rand.Read(pw)
	s.dbPass = hex.EncodeToString(pw)
	t.Cleanup(s.cleanup)

	s.must("network", "create", s.net)
	s.networks = append(s.networks, s.net)
	s.must("volume", "create", s.vol)
	s.volumes = append(s.volumes, s.vol)
	s.containers = append(s.containers, s.db)
	s.must("run", "-d", "--name", s.db, "--network", s.net, "-e", "MARIADB_RANDOM_ROOT_PASSWORD=1", "-e", "MARIADB_DATABASE=wordpress",
		"-e", "MARIADB_USER=wordpress", "-e", "MARIADB_PASSWORD="+s.dbPass, dbImage)
	s.containers = append(s.containers, s.wp)
	s.must(append(append([]string{"run", "-d", "--name", s.wp, "--network", s.net, "--network-alias", "wp.test",
		"-v", s.vol + ":/var/www/html"}, s.dbEnv()...), wpImage)...)

	// The image copies WordPress into the volume and writes wp-config.php;
	// the database takes a moment to accept connections.
	deadline := time.Now().Add(4 * time.Minute)
	for {
		out, err := s.cli("core", "install", "--url="+wpURL, "--title=Moved", "--admin_user=wpadmin",
			"--admin_password="+s.dbPass, "--admin_email=wpadmin@example.com", "--skip-email")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := s.docker("logs", "--tail", "40", s.wp)
			t.Fatalf("WordPress did not start: %v\n%s\n%s", err, out, logs)
		}
		time.Sleep(3 * time.Second)
	}
	s.mustWP("plugin", "install", "wordpress-importer", "--activate")
	// The containers have private addresses, which WordPress never fetches
	// from: let it, as the old site and the new one would be public.
	s.must("exec", s.wp, "sh", "-c", `mkdir -p /var/www/html/wp-content/mu-plugins && printf '%s\n' '<?php' `+
		`"add_filter( 'http_request_host_is_external', '__return_true' );" > /var/www/html/wp-content/mu-plugins/sap13ns-test.php && `+
		`chown -R www-data:www-data /var/www/html/wp-content`)
	return s
}

// startOrigin serves the uploads at originURL/uploads/, as the old site
// would.
func (s *wpStack) startOrigin(uploads string) {
	s.t.Helper()
	s.containers = append(s.containers, s.origin)
	s.must("run", "-d", "--name", s.origin, "--network", s.net, "--network-alias", "origin.test", cliImage,
		"php", "-S", "0.0.0.0:8080", "-t", "/srv")
	s.must("cp", uploads, s.origin+":/srv/uploads")
}

// extract writes the members of an archive under dir.
func extract(t *testing.T, file, dir string) {
	t.Helper()
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type wpPost struct {
	ID     int    `json:"ID"`
	Title  string `json:"post_title"`
	Status string `json:"post_status"`
	Name   string `json:"post_name"`
	Parent int    `json:"post_parent"`
	Author string `json:"post_author"`
}

type wpTerm struct {
	ID     int    `json:"term_id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Parent int    `json:"parent"`
	Count  int    `json:"count"`
}

type wpComment struct {
	ID       string `json:"comment_ID"`
	Parent   string `json:"comment_parent"`
	Approved string `json:"comment_approved"`
	Author   string `json:"comment_author"`
	Content  string `json:"comment_content"`
}

func TestWordPressImport(t *testing.T) {
	if os.Getenv("SAP13NS_WP_TEST") != "1" {
		t.Skip("SAP13NS_WP_TEST=1 imports a backup into WordPress, in Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("SAP13NS_WP_TEST needs docker:", err)
	}
	st := testutil.Store(t)
	ctx := context.Background()
	base := t.TempDir()
	up := filepath.Join(base, "uploads")
	seed(t, st, up)

	for _, mode := range []struct {
		name, wpBase string
	}{{"original addresses", ""}, {"wp-base", wpURL}} {
		t.Run(mode.name, func(t *testing.T) {
			o := siteOpts(filepath.Join(base, "backups-"+strconv.Itoa(len(mode.wpBase))), up, 0)
			o.Site.URL, o.WPBase = originURL, mode.wpBase
			arch, _, err := Create(ctx, st, o)
			if err != nil {
				t.Fatal(err)
			}
			files := filepath.Join(t.TempDir(), "archive")
			extract(t, arch.Path, files)
			uploads := filepath.Join(files, "wp-content", "uploads")

			s := startWordPress(t)
			if mode.wpBase == "" {
				s.startOrigin(uploads) // the old site is still online
			} else {
				// README.txt, step 2: the files go into WordPress first.
				s.must("cp", uploads, s.wp+":/var/www/html/wp-content/uploads/"+export.WPImportDir)
				s.must("exec", s.wp, "chown", "-R", "www-data:www-data", "/var/www/html/wp-content/uploads")
			}
			s.must("cp", filepath.Join(files, NameWXR), s.wp+":/var/www/html/export.xml")
			// As README.txt says, as an administrator, as in the admin pages.
			out := s.mustWP("import", "/var/www/html/export.xml", "--authors=create", "--user=wpadmin")
			t.Logf("wp import:\n%s", out)
			if mode.wpBase != "" {
				// README.txt: the folder can go once imported.
				s.must("exec", s.wp, "rm", "-r", "/var/www/html/wp-content/uploads/"+export.WPImportDir)
			}
			checkWordPress(t, s, mode.wpBase != "", out)
		})
	}
}

func checkWordPress(t *testing.T, s *wpStack, wpBase bool, importLog string) {
	if strings.Contains(importLog, "Failed to import") {
		t.Errorf("the importer failed on part of the file")
	}
	unescape := func(v string) string { return html.UnescapeString(v) }

	// Posts and pages, with their statuses.
	var posts, pages, trash []wpPost
	fields := "--fields=ID,post_title,post_status,post_name,post_parent,post_author"
	s.json(&posts, "post", "list", "--post_type=post", "--post_status=any", fields)
	s.json(&trash, "post", "list", "--post_type=post", "--post_status=trash", fields)
	s.json(&pages, "post", "list", "--post_type=page", "--post_status=any", fields)
	byName := map[string]wpPost{}
	got := map[string]string{}
	for _, p := range slices.Concat(posts, trash, pages) {
		byName[p.Name] = p
		got[p.Name] = unescape(p.Title) + " · " + p.Status
	}
	want := map[string]string{
		"hola": "Hola <mundo> & más · publish", "hello": "Hello world · publish", "borrador": "Borrador · draft",
		"pendiente": "Pendiente · pending", "privado": "Privado · private", "futuro": "Futuro · future",
		"papelera": "Papelera · trash", "sobre": "Sobre · publish", "equipo": "Equipo · publish",
		"sample-page": "Sample Page · publish", "hello-world": "Hello world! · publish", // WordPress's own
		"privacy-policy": "Privacy Policy · draft",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("post %s: %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("posts and pages: %v", got)
	}
	if byName["equipo"].Parent != byName["sobre"].ID {
		t.Errorf("Equipo's parent: %d, want Sobre (%d)", byName["equipo"].Parent, byName["sobre"].ID)
	}

	// Categories with their hierarchy, and tags.
	var cats, tags []wpTerm
	s.json(&cats, "term", "list", "category", "--fields=term_id,name,slug,parent,count")
	s.json(&tags, "term", "list", "post_tag", "--fields=term_id,name,slug,parent,count")
	terms := map[string]wpTerm{}
	for _, c := range cats {
		terms[c.Slug] = c
	}
	if terms["noticias"].Name != "Noticias" || terms["local"].Name != "Local" || terms["local"].Parent != terms["noticias"].ID || terms["noticias"].Parent != 0 {
		t.Errorf("categories: %+v", cats)
	}
	if len(tags) != 1 || tags[0].Slug != "go" || unescape(tags[0].Name) != "Go & Co" {
		t.Errorf("tags: %+v", tags)
	}
	hola := byName["hola"].ID
	var holaCats, holaTags []wpTerm
	s.json(&holaCats, "post", "term", "list", strconv.Itoa(hola), "category", "--fields=term_id,name,slug,parent,count")
	s.json(&holaTags, "post", "term", "list", strconv.Itoa(hola), "post_tag", "--fields=term_id,name,slug,parent,count")
	if len(holaCats) != 1 || holaCats[0].Slug != "local" || len(holaTags) != 1 || holaTags[0].Slug != "go" {
		t.Errorf("Hola's terms: %+v %+v", holaCats, holaTags)
	}

	// Comments: the thread, approved and held; never the spam.
	var comments []wpComment
	s.json(&comments, "comment", "list", "--post_id="+strconv.Itoa(hola), "--status=all",
		"--fields=comment_ID,comment_parent,comment_approved,comment_author,comment_content")
	byAuthor := map[string]wpComment{}
	for _, c := range comments {
		byAuthor[c.Author] = c
	}
	if len(comments) != 3 || byAuthor["Lectora"].Parent != "0" || byAuthor["Edith"].Parent != byAuthor["Lectora"].ID ||
		byAuthor["Otro"].Parent != byAuthor["Edith"].ID || byAuthor["Lectora"].Approved != "1" || byAuthor["Otro"].Approved != "0" ||
		!strings.Contains(byAuthor["Lectora"].Content, "Segundo párrafo") {
		t.Errorf("comments: %+v", comments)
	}

	// Authors, created by the importer.
	var users []struct {
		ID    int    `json:"ID"`
		Login string `json:"user_login"`
		Name  string `json:"display_name"`
	}
	s.json(&users, "user", "list", "--fields=ID,user_login,display_name")
	logins := map[string]int{}
	for _, u := range users {
		logins[u.Login] = u.ID
	}
	for _, l := range []string{"alice", "edith", "arthur"} {
		if logins[l] == 0 {
			t.Errorf("author %s was not created: %+v", l, users)
		}
	}
	if byName["hola"].Author != strconv.Itoa(logins["arthur"]) || byName["sobre"].Author != strconv.Itoa(logins["alice"]) {
		t.Errorf("authors of the posts: hola %s, sobre %s (%v)", byName["hola"].Author, byName["sobre"].Author, logins)
	}

	// Menus and their items.
	var menus []wpTerm
	s.json(&menus, "menu", "list", "--fields=term_id,name,slug,count")
	gotMenus := map[string]int{}
	for _, m := range menus {
		gotMenus[unescape(m.Name)] = m.Count
	}
	if gotMenus["Primary"] != 3 || gotMenus["Primary (en)"] != 1 || gotMenus["Footer"] != 1 || len(menus) != 3 {
		t.Errorf("menus: %v", gotMenus)
	}
	var items []struct {
		Title  string `json:"title"`
		Type   string `json:"type"`
		Object string `json:"object"`
		ID     string `json:"object_id"`
		Link   string `json:"link"`
	}
	s.json(&items, "menu", "item", "list", "primary", "--fields=title,type,object,object_id,link")
	var itemList []string
	for _, it := range items {
		itemList = append(itemList, it.Title+" "+it.Type+" "+it.Object)
		switch it.Title {
		case "Sobre":
			if it.ID != strconv.Itoa(byName["sobre"].ID) {
				t.Errorf("the Sobre item points at %s, want %d", it.ID, byName["sobre"].ID)
			}
		case "Noticias":
			if it.ID != strconv.Itoa(terms["noticias"].ID) {
				t.Errorf("the Noticias item points at %s, want %d", it.ID, terms["noticias"].ID)
			}
		case "Inicio":
			// A custom link: to the new site's home with -wp-base, else to
			// the old site's (as WordPress's own exports do).
			if want := map[bool]string{true: wpURL + "/", false: originURL + "/"}[wpBase]; it.Link != want {
				t.Errorf("the Inicio item links to %s, want %s", it.Link, want)
			}
		}
	}
	sort.Strings(itemList)
	if !slices.Equal(itemList, []string{"Inicio custom custom", "Noticias taxonomy category", "Sobre post_type page"}) {
		t.Errorf("primary menu: %v", itemList)
	}

	// Media, the featured image with its file, and the image in the text.
	var media []wpPost
	s.json(&media, "post", "list", "--post_type=attachment", "--post_status=any", fields)
	if len(media) != 2 {
		t.Errorf("attachments: %+v", media)
	}
	thumb := strings.TrimSpace(s.mustWP("post", "meta", "get", strconv.Itoa(hola), "_thumbnail_id"))
	file := strings.TrimSpace(s.mustWP("eval", "echo get_attached_file("+thumb+");"))
	fileURL := strings.TrimSpace(s.mustWP("eval", "echo wp_get_attachment_url("+thumb+");"))
	if _, err := s.docker("exec", s.wp, "test", "-s", file); err != nil || thumb == "" {
		t.Errorf("the featured image %q has no file at %q", thumb, file)
	}
	if mime := strings.TrimSpace(s.mustWP("post", "get", thumb, "--field=post_mime_type")); mime != "image/png" {
		t.Errorf("the featured image is %q", mime)
	}
	content := s.mustWP("post", "get", strconv.Itoa(hola), "--field=post_content")
	if !strings.Contains(content, `src="`+fileURL+`"`) || strings.Contains(content, originURL+"/uploads/") {
		t.Errorf("Hola's image does not point at the imported file %s:\n%s", fileURL, content)
	}
	// Its link to the page Sobre: to the new site with -wp-base.
	if want := map[bool]string{true: `href="` + wpURL + `/sobre"`, false: `href="` + originURL + `/sobre"`}[wpBase]; !strings.Contains(content, want) {
		t.Errorf("Hola's link to Sobre is not %s:\n%s", want, content)
	}
	if wpBase && strings.Contains(content, "origin.test") {
		t.Errorf("Hola still points at the old site:\n%s", content)
	}
	if !strings.HasPrefix(fileURL, wpURL+"/wp-content/uploads/") {
		t.Errorf("attachment address %s", fileURL)
	}
	t.Logf("featured image: %s (%s)", fileURL, file)
	// The copy keeps the name and folder of the original.
	var orig string
	for _, m := range media {
		if m.Title == "Foto de prueba.png" {
			orig = m.Name
		}
	}
	if !strings.Contains(file, "/wp-content/uploads/20") || strings.Contains(file, export.WPImportDir) || strings.Contains(filepath.Base(file), "-1.") {
		t.Errorf("the featured image was imported at %s (%s)", file, orig)
	}
	// Links between posts go to the site they were written for.
	link := strings.TrimSpace(s.mustWP("eval", fmt.Sprintf("echo get_permalink(%d);", byName["sobre"].ID)))
	t.Logf("Sobre: %s", link)
}

// Command devnetwork sets up a development network of sites
// (docs/MULTISITE.md) to try the portal, single sign-on and the admin's
// site switcher on one machine. Never run it in production: its accounts
// have a known password and skip the second factor.
//
//	make dev-network                  # or: go run ./scripts/devnetwork
//	make dev ENV=data/network.env     # then serve it
//
// It makes its own database (sap13ns_net_dev), env file (data/network.env)
// and data directory (data/network), and serves on port 8139: the portal at
// http://localhost:8139 and the sites at http://<slug>.localhost:8139, which
// browsers resolve to this machine. The single site of `make dev`
// (sap13ns_dev, data/dev.env, port 8137) is never touched.
//
// Running it again on a finished network only prints its addresses; -reset
// drops the database and removes the env file and the data directory
// first. Anything half made is refused, with the command that starts over.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/media"
	"sap13ns/internal/network"
	"sap13ns/internal/store"
	"sap13ns/scripts/internal/devdata"
)

// marker is written in the data directory, so that -reset and a second run
// only ever remove or reuse a directory this command made.
const marker = ".devnetwork"

var (
	dbRe = regexp.MustCompile(`^sap13ns_net_[a-z0-9_]{1,40}$`)
	// The single site's dev server, the owner's other servers and a local
	// llama-server.
	reservedPorts = []int{8080, 8137, 8145}
)

type options struct {
	db, env, data, pgHost string
	port                  int
	reset                 bool
}

func main() {
	log.SetFlags(0)
	var o options
	flag.StringVar(&o.db, "db", "sap13ns_net_dev", "database to create (sap13ns_net_*)")
	flag.IntVar(&o.port, "port", 8139, "port of the network")
	flag.StringVar(&o.env, "env", "data/network.env", "env file to write")
	flag.StringVar(&o.data, "data", "data/network", "data directory of the network")
	flag.StringVar(&o.pgHost, "pghost", "/tmp", "PostgreSQL host or socket directory")
	flag.BoolVar(&o.reset, "reset", false, "drop the database and remove the env file and data directory first")
	flag.Parse()
	if err := run(context.Background(), o); err != nil {
		log.Fatal("devnetwork: ", err)
	}
}

func run(ctx context.Context, o options) error {
	if err := o.check(); err != nil {
		return err
	}
	// The env file decides everything: a variable exported in the shell
	// (one of data/dev.env's, say) would win over it.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SAP13NS_") {
			os.Unsetenv(k)
		}
	}
	admin, err := pgx.Connect(ctx, (&url.URL{Scheme: "postgres", Path: "/postgres", RawQuery: url.Values{"host": {o.pgHost}}.Encode()}).String())
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer admin.Close(context.Background())
	if o.reset {
		if err := o.remove(ctx, admin); err != nil {
			return err
		}
	}

	exists, err := dbExists(ctx, admin, o.db)
	if err != nil {
		return err
	}
	_, envErr := os.Stat(o.env)
	_, dataErr := os.Stat(o.data)
	_, markErr := os.Stat(filepath.Join(o.data, marker))
	fresh := !exists && errors.Is(envErr, os.ErrNotExist) && errors.Is(dataErr, os.ErrNotExist)
	again := exists && envErr == nil && markErr == nil
	if !fresh && !again {
		return fmt.Errorf("a network is half made here (database %s %s, %s %s, %s %s).\nStart it over with: %s",
			o.db, there(exists), rel(o.env), there(envErr == nil), rel(o.data), there(markErr == nil), o.startOver())
	}
	if fresh {
		if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{o.db}.Sanitize()); err != nil {
			return fmt.Errorf("create the database %s: %w", o.db, err)
		}
		if err := os.MkdirAll(o.data, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(o.data, marker), []byte("made by scripts/devnetwork\n"), 0o640); err != nil {
			return err
		}
		if err := o.writeEnv(); err != nil {
			return err
		}
	}

	if err := config.LoadEnvFile(o.env); err != nil {
		return err
	}
	cfg, err := config.Load(true)
	if err != nil {
		return err
	}
	if err := o.checkConfig(cfg); err != nil {
		return err
	}
	pool, err := db.OpenShared(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := network.Migrate(ctx, pool, discard()); err != nil {
		return err
	}
	n, err := network.New(pool, cfg, discard())
	if err != nil {
		return err
	}
	if _, err := n.InitNetworkSite(ctx, cfg.BaseURL.Hostname()); err != nil {
		return err
	}
	s := &seeder{ctx: ctx, cfg: cfg, pool: pool, n: n, portal: store.New(n.DB())}
	if err := s.portalSite(fresh); err != nil {
		return err
	}
	if err := s.sites(); err != nil {
		return err
	}
	return s.report(o, fresh)
}

func (o *options) check() error {
	switch {
	case !dbRe.MatchString(o.db):
		return fmt.Errorf("-db %q: the name of a development network starts with sap13ns_net_ (lower case, digits, _)", o.db)
	case o.port < 1024 || o.port > 65535:
		return fmt.Errorf("-port %d: use a free port above 1023", o.port)
	case slices.Contains(reservedPorts, o.port):
		return fmt.Errorf("-port %d is reserved here (8137: the single site of make dev; 8145; 8080: a local llama-server)", o.port)
	case filepath.Ext(o.env) != ".env":
		return fmt.Errorf("-env %q: an env file ends with .env", o.env)
	}
	env, err := filepath.Abs(o.env)
	if err != nil {
		return err
	}
	data, err := filepath.Abs(o.data)
	if err != nil {
		return err
	}
	for _, own := range []string{"data/dev.env", "data/site.env", ".env"} {
		if p, _ := filepath.Abs(own); p == env {
			return fmt.Errorf("-env %s belongs to another server", o.env)
		}
	}
	wd, _ := os.Getwd()
	for _, own := range []string{".", "data", "data/uploads", "data/plugins", "data/themes", "data/cache", "data/site"} {
		if p, _ := filepath.Abs(own); p == data {
			return fmt.Errorf("-data %s is the data of another server; use a directory of its own, such as data/network", o.data)
		}
	}
	if rel, err := filepath.Rel(data, wd); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("-data %s holds the working directory", o.data)
	}
	o.env, o.data = env, data
	return nil
}

// checkConfig makes sure the env file is the one this command wrote for
// these options.
func (o *options) checkConfig(cfg *config.Config) error {
	u, err := url.Parse(cfg.DatabaseURL)
	switch {
	case err != nil || strings.TrimPrefix(u.Path, "/") != o.db:
		return fmt.Errorf("%s points to another database than %s", o.env, o.db)
	case !cfg.Multisite || !cfg.Dev || !cfg.DevSkip2FA:
		return fmt.Errorf("%s is not a development network (SAP13NS_MULTISITE, SAP13NS_DEV, SAP13NS_DEV_SKIP_2FA)", o.env)
	case cfg.BaseURL.Port() != strconv.Itoa(o.port) || cfg.Listen != "127.0.0.1:"+strconv.Itoa(o.port):
		return fmt.Errorf("%s serves on %s, not on port %d", o.env, cfg.Listen, o.port)
	case cfg.DataDir != o.data:
		return fmt.Errorf("%s keeps its data in %s, not in %s", o.env, cfg.DataDir, o.data)
	}
	return nil
}

func (o *options) writeEnv() error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	dbURL := (&url.URL{Scheme: "postgres", Path: "/" + o.db, RawQuery: url.Values{"host": {o.pgHost}}.Encode()}).String()
	port := strconv.Itoa(o.port)
	env := strings.Join([]string{
		"# A development network of sites, made by scripts/devnetwork (make dev-network).",
		"# Serve it with: make dev ENV=" + rel(o.env),
		"SAP13NS_DATABASE_URL=" + dbURL,
		"SAP13NS_SECRET_KEY=" + hex.EncodeToString(key),
		"SAP13NS_MULTISITE=true",
		"# The portal; the sites are <slug>.localhost on the same port.",
		"SAP13NS_BASE_URL=http://localhost:" + port,
		"SAP13NS_SITES_DOMAIN=localhost",
		"SAP13NS_LISTEN=127.0.0.1:" + port,
		"SAP13NS_DATA_DIR=" + o.data,
		"SAP13NS_DEV=true",
		"SAP13NS_DEV_SKIP_2FA=true",
		"SAP13NS_LOG_FORMAT=text",
		"SAP13NS_DB_MAX_CONNS=16",
		"",
	}, "\n")
	if err := os.MkdirAll(filepath.Dir(o.env), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(o.env, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(env); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// remove undoes an earlier run: the database, the env file and the data
// directory, which must carry the marker.
func (o *options) remove(ctx context.Context, admin *pgx.Conn) error {
	if _, err := os.Stat(o.data); err == nil {
		if _, err := os.Stat(filepath.Join(o.data, marker)); err != nil {
			return fmt.Errorf("%s was not made by this command (no %s in it): remove it yourself", o.data, marker)
		}
		if err := os.RemoveAll(o.data); err != nil {
			return err
		}
	}
	if err := os.Remove(o.env); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{o.db}.Sanitize()+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("drop the database %s: %w", o.db, err)
	}
	fmt.Printf("removed the database %s, %s and %s\n", o.db, rel(o.env), rel(o.data))
	return nil
}

// startOver is the command that runs this one again with -reset.
func (o options) startOver() string {
	d := options{db: "sap13ns_net_dev", port: 8139, env: "data/network.env", data: "data/network", pgHost: "/tmp"}
	if o.db == d.db && o.port == d.port && rel(o.env) == d.env && rel(o.data) == d.data && o.pgHost == d.pgHost {
		return "make dev-network RESET=1"
	}
	return fmt.Sprintf("go run ./scripts/devnetwork -reset -db %s -port %d -env %s -data %s -pghost %s",
		o.db, o.port, rel(o.env), rel(o.data), o.pgHost)
}

func there(ok bool) string {
	if ok {
		return "exists"
	}
	return "missing"
}

func dbExists(ctx context.Context, admin *pgx.Conn, name string) (bool, error) {
	var ok bool
	err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

// rel is a path as the working directory sees it.
func rel(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	if r, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// ------------------------------------------------------------- content --

type account struct {
	username, email, name, role, bio string
	networkAdmin                     bool
}

var accounts = []account{
	{"admin", "admin@example.org", "Lucía Ferrer", auth.RoleAdmin, "Administra la red de desarrollo.", true},
	{"marta", "marta@example.org", "Marta Gil", auth.RoleSubscriber, "Cocina los domingos.", false},
}

// A site of the network: its owner, the other members and their roles,
// and its content.
type siteSpec struct {
	slug, owner string
	members     map[string]string // username: role
	fill        func(s *seeder, st *store.Store, proc *media.Processor, users map[string]int64) error
}

var specs = []siteSpec{
	{slug: "cuaderno", owner: "admin", members: map[string]string{"marta": auth.RoleEditor},
		fill: func(s *seeder, st *store.Store, proc *media.Processor, users map[string]int64) error {
			return devdata.Blog(s.ctx, st, proc, users["admin"], users["marta"])
		}},
	{slug: "recetas", owner: "marta", members: map[string]string{"admin": auth.RoleEditor},
		fill: func(s *seeder, st *store.Store, proc *media.Processor, users map[string]int64) error {
			return devdata.Fill(s.ctx, st, proc, users["marta"], recipes)
		}},
	{slug: "studio", owner: "admin",
		fill: func(s *seeder, st *store.Store, proc *media.Processor, users map[string]int64) error {
			return devdata.Fill(s.ctx, st, proc, users["admin"], studio)
		}},
}

type seeder struct {
	ctx    context.Context
	cfg    *config.Config
	pool   *pgxpool.Pool
	n      *network.Network
	portal *store.Store
	ids    map[string]int64 // accounts by username
}

// portalSite makes the portal's accounts and, on a fresh network, its
// settings: in Spanish, taking requests for sites.
func (s *seeder) portalSite(fresh bool) error {
	s.ids = map[string]int64{}
	for _, a := range accounts {
		u, err := s.portal.UserByLogin(s.ctx, a.username)
		switch {
		case err == nil:
			s.ids[a.username] = u.ID
		case errors.Is(err, store.ErrNotFound):
			id, err := devdata.User(s.ctx, s.portal, a.username, a.email, a.name, a.role, a.bio)
			if err != nil {
				return fmt.Errorf("account %s: %w", a.username, err)
			}
			s.ids[a.username] = id
		default:
			return err
		}
		if a.networkAdmin {
			if err := s.n.AddNetworkAdmin(s.ctx, s.ids[a.username], 0); err != nil {
				return err
			}
		}
	}
	if !fresh {
		return nil
	}
	set, err := s.portal.LoadSettings(s.ctx)
	if err != nil {
		return err
	}
	set.SiteTitle = "Red sap13ns"
	set.Tagline = "La red de sitios de desarrollo."
	set.AllowRegistration = true
	return s.portal.SaveSettings(s.ctx, set)
}

// sites creates the sites that are missing, with their members, their
// members' users (as their first sign-on through the portal would) and
// their content.
func (s *seeder) sites() error {
	for _, spec := range specs {
		site, err := s.n.SiteBySlug(s.ctx, spec.slug)
		if err == nil {
			continue // made by an earlier run
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if site, err = s.n.CreateSite(s.ctx, network.NewSite{Slug: spec.slug, OwnerID: s.ids[spec.owner], Plan: "pro"}); err != nil {
			return fmt.Errorf("site %s: %w", spec.slug, err)
		}
		roles := map[string]string{spec.owner: auth.RoleAdmin}
		for who, role := range spec.members {
			if err := s.n.SetMember(s.ctx, site.ID, s.ids[who], role, s.ids[spec.owner]); err != nil {
				return err
			}
			roles[who] = role
		}
		h, err := db.Scope(s.pool, site.Schema)
		if err != nil {
			return err
		}
		st := store.New(h)
		users := map[string]int64{}
		for who, role := range roles {
			a := accountOf(who)
			id, err := st.CreateSSOUser(s.ctx, store.NewSSOUser{NetworkAccountID: s.ids[who], Username: a.username, Email: a.email,
				DisplayName: a.name, Role: role})
			if err != nil {
				return fmt.Errorf("site %s, user %s: %w", spec.slug, who, err)
			}
			users[who] = id
		}
		scfg, err := s.n.SiteConfig(s.ctx, s.cfg, site)
		if err != nil {
			return err
		}
		proc := &media.Processor{Dir: scfg.UploadsDir, MaxBytes: scfg.MaxUploadBytes}
		if err := spec.fill(s, st, proc, users); err != nil {
			return fmt.Errorf("site %s: %w", spec.slug, err)
		}
	}
	return nil
}

func accountOf(username string) account {
	for _, a := range accounts {
		if a.username == username {
			return a
		}
	}
	panic("no account " + username)
}

func (s *seeder) report(o options, fresh bool) error {
	if fresh {
		fmt.Printf("created the development network: database %s, %s, %s\n\n", o.db, rel(o.env), rel(o.data))
	} else {
		fmt.Printf("the development network is already set up (database %s, %s)\n\n", o.db, rel(o.env))
	}
	fmt.Printf("  portal      %s/admin/\n", s.cfg.BaseURL)
	for _, spec := range specs {
		site, err := s.n.SiteBySlug(s.ctx, spec.slug)
		if err != nil {
			return err
		}
		title, err := s.n.SiteTitle(s.ctx, site)
		if err != nil {
			return err
		}
		fmt.Printf("  %-11s %s://%s:%d/  %s, owned by %s\n", spec.slug, s.cfg.BaseURL.Scheme, site.PrimaryHost, o.port, title, spec.owner)
	}
	fmt.Printf("\n  accounts    admin (network administrator) and marta, password %s, no second factor\n", devdata.Password)
	fmt.Printf("  serve it    make dev ENV=%s   (or ./bin/sap13ns serve -env %s)\n", rel(o.env), rel(o.env))
	fmt.Printf("  start over  %s\n", o.startOver())
	return nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ------------------------------------------------------------ the sites --

var recipes = devdata.Simple{
	Title:   "Recetas de casa",
	Tagline: "Platos sencillos para cada día de la semana.",
	Footer:  "© 2026 Recetas de casa",
	Lang:    "es",
	Theme:   "bento",
	Alt:     "Fotografía abstracta de colores cálidos",
	Posts: []devdata.Post{
		{Title: "Tortilla de patatas jugosa", Slug: "tortilla-de-patatas", Category: "Platos", Tags: []string{"huevos", "clásicos"}, DaysAgo: 1, Hue: 35,
			Excerpt: "El truco está en confitar la patata a fuego lento y no cuajar del todo el huevo.",
			Body:    "## Ingredientes\n\n- 4 patatas medianas\n- 6 huevos\n- 1 cebolla (opcional)\n- Aceite de oliva y sal\n\n## Preparación\n\n1. Corta la patata en láminas finas y confítala 20 minutos.\n2. Escurre y mezcla con el huevo batido.\n3. Cuaja un minuto por cada lado.\n\n> Si queda líquida por dentro, está perfecta."},
		{Title: "Lentejas de la abuela", Slug: "lentejas", Category: "Guisos", Tags: []string{"legumbres", "invierno"}, DaysAgo: 4, Hue: 20,
			Excerpt: "Un guiso de cuchara que mejora de un día para otro.",
			Body:    "Sofríe cebolla, pimiento y zanahoria; añade pimentón, las lentejas y agua fría. **Cuarenta minutos** a fuego suave y listo."},
		{Title: "Gazpacho en cinco minutos", Slug: "gazpacho", Category: "Platos", Tags: []string{"verano", "sin cocción"}, DaysAgo: 9, Hue: 5,
			Excerpt: "Tomate maduro, un buen aceite y una batidora.",
			Body:    "Tritura tomate, pepino, pimiento, ajo, pan del día anterior, aceite, vinagre y sal. Sirve muy frío."},
	},
	About:   struct{ Title, Slug, Body string }{"Sobre las recetas", "sobre", "Un recetario de ejemplo de la red de desarrollo de **sap13ns**."},
	Menu:    [][2]string{{"Inicio", "/"}, {"Platos", "/category/platos"}, {"Guisos", "/category/guisos"}, {"Sobre", "/sobre"}},
	Widgets: [][2]string{{"recent_posts", "Lo último"}, {"categories", "Categorías"}},
}

var studio = devdata.Simple{
	Title:   "Northwind Studio",
	Tagline: "A small design studio that writes about its work.",
	Footer:  "© 2026 Northwind Studio",
	Lang:    "en",
	Theme:   "glassmorphism",
	Alt:     "An abstract gradient",
	Posts: []devdata.Post{
		{Title: "Designing for slow networks", Slug: "slow-networks", Category: "Notes", Tags: []string{"performance"}, DaysAgo: 2, Hue: 200,
			Excerpt: "What we learnt shipping a site that loads in one round trip.",
			Body:    "Most visitors are not on fibre. We budget **14 KB** for the first response and inline nothing that can wait."},
		{Title: "A colour system in OKLCH", Slug: "oklch-colours", Category: "Design", Tags: []string{"colour", "css"}, DaysAgo: 6, Hue: 280,
			Excerpt: "Perceptual lightness makes light and dark themes behave.",
			Body:    "We define every token in `oklch()` and derive the dark theme by moving lightness only. Contrast stays predictable."},
		{Title: "Case study: a museum's new site", Slug: "museum-case-study", Category: "Work", Tags: []string{"case study"}, DaysAgo: 12, Hue: 160,
			Excerpt: "From forty templates to five, and a CMS the curators like.",
			Body:    "## The brief\n\nForty page templates, three CMSs and nobody who dared to change the home page.\n\n## What we did\n\n- Five templates\n- One design system\n- Writing guidelines the curators wrote with us"},
	},
	About:   struct{ Title, Slug, Body string }{"About the studio", "about", "Northwind Studio is a sample site of the **sap13ns** development network."},
	Menu:    [][2]string{{"Home", "/"}, {"Work", "/category/work"}, {"Notes", "/category/notes"}, {"About", "/about"}},
	Widgets: [][2]string{{"recent_posts", "Latest"}, {"tags", "Tags"}},
}

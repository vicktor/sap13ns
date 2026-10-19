package main

// The commands of a multisite install (SAP13NS_MULTISITE=true): network
// init, site …, the multisite paths of migrate and serve, and -site for
// the commands that work on one site.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/network"
	"sap13ns/internal/store"
	"sap13ns/internal/update"
	"sap13ns/internal/web"
)

// out is where the network commands print; tests read it.
var out io.Writer = os.Stdout

var errNotNetwork = errors.New("this command needs a network (SAP13NS_MULTISITE=true)")

// openNetwork opens the shared pool of a network and migrates the network
// schema (with the portal's tables).
func openNetwork(ctx context.Context, cfg *config.Config, log *slog.Logger, conns int32) (*pgxpool.Pool, *network.Network, error) {
	if !cfg.Multisite {
		return nil, nil, errNotNetwork
	}
	pool, err := db.OpenShared(ctx, cfg.DatabaseURL, conns)
	if err != nil {
		return nil, nil, err
	}
	if err := network.Migrate(ctx, pool, log); err != nil {
		pool.Close()
		return nil, nil, err
	}
	n, err := network.New(pool, cfg, log)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, n, nil
}

// migrateSites applies the site migrations to the schema of every tenant
// in use (pending, active, suspended), or of the one named slug. The
// portal's schema is migrated by openNetwork. It goes through every site
// and reports all failures.
func migrateSites(ctx context.Context, pool *pgxpool.Pool, n *network.Network, log *slog.Logger, slug string) error {
	var sites []network.Site
	if slug != "" {
		s, err := siteBySlug(ctx, n, slug)
		if err != nil {
			return err
		}
		if s.Kind == network.KindNetwork {
			return nil
		}
		sites = []network.Site{*s}
	} else {
		var err error
		if sites, err = n.ServedSites(ctx); err != nil {
			return err
		}
	}
	set, err := db.SiteMigrations()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sites {
		if err := db.MigrateSchema(ctx, pool, s.Schema, set, log.With("site", s.ID)); err != nil {
			errs = append(errs, fmt.Errorf("site %s (%s): %w", s.Slug, s.Schema, err))
		}
	}
	return errors.Join(errs...)
}

func siteBySlug(ctx context.Context, n *network.Network, slug string) (*network.Site, error) {
	s, err := n.SiteBySlug(ctx, strings.ToLower(strings.TrimSpace(slug)))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no site %q", slug)
	}
	return s, err
}

// target is the site a command works on: the single site, or on a network
// the one named by -site.
type target struct {
	cfg   *config.Config // the site's
	store *store.Store
	site  *network.Site // nil for a single site
	close func()
}

// openTarget opens the site a command works on. A single site keeps what
// the commands always did (its database, migrated); a network needs -site,
// and "network" names the portal.
func openTarget(ctx context.Context, cfg *config.Config, log *slog.Logger, slug string, conns int32) (*target, error) {
	if !cfg.Multisite {
		if slug != "" {
			return nil, errors.New("-site works on a network only (SAP13NS_MULTISITE=true)")
		}
		pool, err := db.Open(ctx, cfg.DatabaseURL, conns)
		if err != nil {
			return nil, err
		}
		if err := db.Migrate(ctx, pool, log); err != nil {
			pool.Close()
			return nil, err
		}
		return &target{cfg: cfg, store: store.New(pool), close: pool.Close}, nil
	}
	if slug == "" {
		return nil, errors.New("this is a network: name the site with -site SLUG (-site network for the portal)")
	}
	pool, n, err := openNetwork(ctx, cfg, log, conns)
	if err != nil {
		return nil, err
	}
	t, err := func() (*target, error) {
		s, err := siteBySlug(ctx, n, slug)
		if err != nil {
			return nil, err
		}
		if s.Status == network.StatusDeleted || s.Status == network.StatusProvisioning {
			return nil, fmt.Errorf("site %s is %s", s.Slug, s.Status)
		}
		if err := migrateSites(ctx, pool, n, log, s.Slug); err != nil {
			return nil, err
		}
		scfg, err := n.SiteConfig(ctx, cfg, s)
		if err != nil {
			return nil, err
		}
		h, err := db.Scope(pool, s.Schema)
		if err != nil {
			return nil, err
		}
		return &target{cfg: scfg, store: store.New(h), site: s, close: pool.Close}, nil
	}()
	if err != nil {
		pool.Close()
		return nil, err
	}
	return t, nil
}

// serveNetwork runs a network: the shared pool, every schema migrated, the
// registry of sites and its host router.
func serveNetwork(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if cfg.MemoryLimitMB > 0 && os.Getenv("GOMEMLIMIT") == "" {
		// The GC works harder near the limit; the registry unloads sites
		// before it is reached.
		debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) << 20)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	pool, n, err := openNetwork(ctx, cfg, log, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrateSites(ctx, pool, n, log, ""); err != nil {
		return err
	}
	checker := update.NewChecker(web.Version, nil, log)
	reg, err := network.NewRegistry(network.RegistryOptions{Config: cfg, Network: n, Log: log, UI: uiFS(cfg), Updates: checker})
	if err != nil {
		return err
	}
	if err := reg.Start(ctx); err != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		return errors.Join(err, reg.Close(cctx))
	}
	go checker.Run(ctx)
	return runHTTP(ctx, cfg, log, reg, func(sctx context.Context) {
		if err := reg.Close(sctx); err != nil {
			log.Error("close the sites", "err", err)
		}
	})
}

// ---------------------------------------------------------------- network --

const networkUsage = "usage: sap13ns network init [-username U -email E [-name N]]"

// networkCmd implements "sap13ns network init": the network schema, the
// portal's site (on the SAP13NS_BASE_URL host) and a network admin.
func networkCmd(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New(networkUsage)
	}
	var username, email, name string
	cfg, log, _, err := setup("network init", args[1:], true, func(f *flag.FlagSet) {
		f.StringVar(&username, "username", "", "username of a network administrator to create")
		f.StringVar(&email, "email", "", "their e-mail address")
		f.StringVar(&name, "name", "", "their public name")
	})
	if err != nil {
		return err
	}
	username = strings.ToLower(strings.TrimSpace(username))
	ctx := context.Background()
	pool, n, err := openNetwork(ctx, cfg, log, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	site, err := n.InitNetworkSite(ctx, cfg.BaseURL.Hostname())
	if err != nil {
		return err
	}
	admins, err := n.NetworkAdmins(ctx)
	if err != nil {
		return err
	}
	if username == "" {
		if len(admins) == 0 {
			return errors.New("the network has no administrator yet: give -username and -email")
		}
		fmt.Fprintf(out, "network site #%d on %s; administrators: %d\n", site.ID, cfg.BaseURL, len(admins))
		return nil
	}
	id, pw, generated, err := createUser(ctx, store.New(n.DB()), username, email, name, auth.RoleAdmin)
	if err != nil {
		return err
	}
	if err := n.AddNetworkAdmin(ctx, id, 0); err != nil {
		return err
	}
	fmt.Fprintf(out, "network site #%d on %s\ncreated network administrator %s (#%d)\n", site.ID, cfg.BaseURL, username, id)
	if generated {
		fmt.Fprintf(out, "password: %s\n", pw)
	}
	return nil
}

// createUser creates a local user with a password read from stdin, or a
// generated one (then returned to print once).
func createUser(ctx context.Context, st *store.Store, username, email, name, role string) (int64, string, bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	switch {
	case username == "":
		return 0, "", false, errors.New("-username is required")
	case email == "":
		return 0, "", false, errors.New("-email is required")
	case !auth.ValidRole(role):
		return 0, "", false, fmt.Errorf("invalid role %q", role)
	}
	pw, generated, err := readPassword()
	if err != nil {
		return 0, "", false, err
	}
	if err := auth.CheckPasswordPolicy(pw, username, email); err != nil {
		return 0, "", false, err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return 0, "", false, err
	}
	id, err := st.CreateUser(ctx, store.NewUser{Username: username, Email: strings.ToLower(email), DisplayName: name, Role: role, PasswordHash: hash})
	if err != nil {
		return 0, "", false, err
	}
	st.Audit(ctx, id, "user.created_cli", username, netipZero, map[string]string{"role": role})
	return id, pw, generated, nil
}

// ------------------------------------------------------------------- site --

const siteUsage = `usage: sap13ns site create -slug S -owner EMAIL [-plan P] [-title T] [-lang L]
       sap13ns site list [-status S] [-json]
       sap13ns site suspend -slug S -reason R | resume -slug S | delete -slug S -yes
       sap13ns site domain add -slug S HOST | verify HOST | primary HOST | remove HOST`

// siteCmd implements "sap13ns site …". Running servers pick the changes
// up at once (they listen on sap13ns_network).
func siteCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(siteUsage)
	}
	sub, args := args[0], args[1:]
	if sub == "domain" {
		if len(args) == 0 {
			return errors.New(siteUsage)
		}
		sub, args = "domain "+args[0], args[1:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			args = append(args[1:], args[0]) // the host may come before the flags
		}
	}
	var slug, owner, plan, title, lang, status, reason string
	var asJSON, yes bool
	cfg, log, fs, err := setup("site "+sub, args, true, func(f *flag.FlagSet) {
		switch sub {
		case "create", "suspend", "resume", "delete", "domain add":
			f.StringVar(&slug, "slug", "", "the site's name (its subdomain)")
		}
		switch sub {
		case "create":
			f.StringVar(&owner, "owner", "", "e-mail address of the owner's network account")
			f.StringVar(&plan, "plan", "personal", "plan")
			f.StringVar(&title, "title", "", "site title")
			f.StringVar(&lang, "lang", "", "main language, such as es or en")
		case "list":
			f.StringVar(&status, "status", "", "only sites with this status")
			f.BoolVar(&asJSON, "json", false, "print JSON")
		case "suspend":
			f.StringVar(&reason, "reason", "", "why, shown to the owner")
		case "delete":
			f.BoolVar(&yes, "yes", false, "confirm")
		}
	})
	if err != nil {
		return err
	}
	if !cfg.Multisite {
		return errNotNetwork
	}
	ctx := context.Background()
	pool, n, err := openNetwork(ctx, cfg, log, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	portal := store.New(n.DB())

	host := func() (string, error) {
		if fs.NArg() != 1 {
			return "", errors.New("usage: sap13ns site " + sub + " HOST")
		}
		return network.CanonicalHost(strings.ToLower(fs.Arg(0)))
	}
	site := func() (*network.Site, error) {
		if slug == "" {
			return nil, errors.New("-slug is required")
		}
		return siteBySlug(ctx, n, slug)
	}

	switch sub {
	case "create":
		return siteCreate(ctx, pool, n, portal, slug, owner, plan, title, lang)
	case "list":
		return siteList(ctx, n, portal, status, asJSON)
	case "suspend", "resume", "delete":
		s, err := site()
		if err != nil {
			return err
		}
		switch sub {
		case "suspend":
			if strings.TrimSpace(reason) == "" {
				return errors.New("-reason is required: the owner sees it")
			}
			err = n.SuspendSite(ctx, s.ID, strings.TrimSpace(reason))
		case "resume":
			err = n.ResumeSite(ctx, s.ID)
		case "delete":
			if !yes {
				return fmt.Errorf("deleting %s stops serving it at once; its data stays until purged. Add -yes to go on", s.Slug)
			}
			err = n.DeleteSite(ctx, s.ID)
		}
		if err != nil {
			return err
		}
		if s, err = n.Site(ctx, s.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "site %s (#%d) is %s\n", s.Slug, s.ID, s.Status)
		return nil
	case "domain add":
		s, err := site()
		if err != nil {
			return err
		}
		h, err := host()
		if err != nil {
			return err
		}
		d, err := n.AddDomain(ctx, s.ID, h)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "added %s to %s; it is routed once verified. The owner proves control with the DNS record\n"+
			"  _sap13ns-verify.%s TXT \"sap13ns-site-verification=%s\"\n"+
			"and points the host to the network. Then run: sap13ns site domain verify %s\n", d.Host, s.Slug, d.Host, d.VerifyToken, d.Host)
		return nil
	case "domain verify", "domain primary", "domain remove":
		h, err := host()
		if err != nil {
			return err
		}
		switch sub {
		case "domain verify":
			// The DNS check is the operator's for now; this records it.
			err = n.VerifyDomain(ctx, h)
		case "domain primary":
			err = n.SetPrimaryDomain(ctx, h)
		case "domain remove":
			err = n.RemoveDomain(ctx, h)
		}
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no site has the host %s", h)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s done\n", h, strings.TrimPrefix(sub, "domain "))
		return nil
	}
	return errors.New(siteUsage)
}

func siteCreate(ctx context.Context, pool *pgxpool.Pool, n *network.Network, portal *store.Store, slug, owner, plan, title, lang string) error {
	switch {
	case slug == "":
		return errors.New("-slug is required")
	case owner == "":
		return errors.New("-owner is required: the e-mail address of a network account")
	case lang != "" && !web.ValidLanguage(lang):
		return fmt.Errorf("unknown language %q", lang)
	}
	u, err := portal.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(owner)))
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no network account has the e-mail address %s; create one with sap13ns user create -site network", owner)
	} else if err != nil {
		return err
	}
	s, err := n.CreateSite(ctx, network.NewSite{Slug: strings.ToLower(strings.TrimSpace(slug)), OwnerID: u.ID, Plan: plan})
	if err != nil {
		return err
	}
	if title != "" || lang != "" {
		h, err := db.Scope(pool, s.Schema)
		if err != nil {
			return err
		}
		st := store.New(h)
		set, err := st.LoadSettings(ctx)
		if err != nil {
			return err
		}
		if title != "" {
			set.SiteTitle = title
		}
		if lang != "" {
			set.Language = lang
		}
		if err := st.SaveSettings(ctx, set); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "created site %s (#%d, %s, plan %s) at %s, owned by %s\n", s.Slug, s.ID, s.Status, s.Plan, s.PrimaryHost, u.Email)
	if s.Status == network.StatusPending {
		fmt.Fprintln(out, "its plan is approved by hand: it opens once a network administrator approves it")
	}
	return nil
}

// siteView is what `site list -json` prints: never the sealed secret.
type siteView struct {
	ID              int64      `json:"id"`
	Kind            string     `json:"kind"`
	Slug            string     `json:"slug"`
	Status          string     `json:"status"`
	Plan            string     `json:"plan,omitempty"`
	Owner           string     `json:"owner,omitempty"`
	Host            string     `json:"host"`
	SuspendedReason string     `json:"suspended_reason,omitempty"`
	StorageBytes    int64      `json:"storage_bytes"`
	CreatedAt       time.Time  `json:"created_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

func siteList(ctx context.Context, n *network.Network, portal *store.Store, status string, asJSON bool) error {
	switch status {
	case "", network.StatusProvisioning, network.StatusPending, network.StatusActive, network.StatusSuspended, network.StatusDeleted:
	default:
		return fmt.Errorf("unknown status %q", status)
	}
	owners := map[int64]string{}
	var views []siteView
	for offset := 0; ; {
		page, total, err := n.ListSites(ctx, network.SiteQuery{Status: status, Limit: 1000, Offset: offset})
		if err != nil {
			return err
		}
		for _, s := range page {
			owner, ok := owners[s.OwnerID]
			if !ok && s.OwnerID != 0 {
				if u, err := portal.UserByID(ctx, s.OwnerID); err == nil {
					owner = u.Email
				}
				owners[s.OwnerID] = owner
			}
			views = append(views, siteView{ID: s.ID, Kind: s.Kind, Slug: s.Slug, Status: s.Status, Plan: s.Plan, Owner: owner,
				Host: s.PrimaryHost, SuspendedReason: s.SuspendedReason, StorageBytes: s.StorageBytes, CreatedAt: s.CreatedAt, DeletedAt: s.DeletedAt})
		}
		offset += len(page)
		if len(page) == 0 || offset >= total {
			break
		}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(orEmpty(views))
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSLUG\tSTATUS\tPLAN\tOWNER\tHOST\tCREATED")
	for _, v := range views {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Slug, v.Status, or(v.Plan, "-"), termSafe(or(v.Owner, "-")), v.Host,
			v.CreatedAt.Local().Format(time.DateOnly))
	}
	return tw.Flush()
}

// orEmpty makes an empty list print as [], not null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

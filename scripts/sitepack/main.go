// Command sitepack loads a content pack (site/content: site.json plus
// <lang>/*.md pages and posts) into a sap13ns database. It is idempotent:
// run it again after editing the Markdown and only the changed items are
// updated. See internal/contentpack for the format.
//
//	go run ./scripts/sitepack -env data/site.env site/content
//
// It applies the database migrations first, so it also works on an empty
// database. When the database has no users and SAP13NS_DEV is true it
// creates the user "admin" with the password "dev-password-123"; never do
// that in production (create a user with `sap13ns user create` instead).
//
// A running server keeps the site settings in memory and caches pages: restart
// it after loading a pack so that new settings, menus and pages show up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/contentpack"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

func main() {
	env := flag.String("env", "data/site.env", "env file with the SAP13NS_* settings")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: go run ./scripts/sitepack [-env file] [pack dir, default site/content]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	dir := "site/content"
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	if err := run(*env, dir); err != nil {
		log.Fatal(err)
	}
}

func run(envFile, dir string) error {
	if err := config.LoadEnvFile(envFile); err != nil {
		return err
	}
	cfg, err := config.Load(false)
	if err != nil {
		return err
	}

	// Parse before touching the database so that a broken pack changes nothing.
	items, err := contentpack.Parse(dir)
	if err != nil {
		return err
	}
	var site *contentpack.Site
	sitePath := filepath.Join(dir, "site.json")
	if _, err := os.Stat(sitePath); err == nil {
		if site, err = contentpack.LoadSite(sitePath); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s not found: keeping the current settings\n", sitePath)
	}

	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := db.Migrate(ctx, pool, logger); err != nil {
		return err
	}
	st := store.New(pool)

	authorID, err := author(ctx, st, cfg.Dev)
	if err != nil {
		return err
	}
	rep, err := contentpack.Import(ctx, st, items, contentpack.Options{AuthorID: authorID, Site: site, Now: time.Now()})
	if rep != nil {
		fmt.Print(rep.String())
	}
	if err != nil {
		return err
	}
	fmt.Println("done; restart a running server to pick up settings and menus")
	return nil
}

// author returns the user that signs the imported content: "admin" when it is
// an administrator, otherwise the oldest administrator. On an empty
// development database it creates admin / dev-password-123.
func author(ctx context.Context, st *store.Store, dev bool) (int64, error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		if !dev {
			return 0, errors.New("the database has no users: create an administrator first with `sap13ns user create -username … -email … -role admin`")
		}
		hash, err := auth.HashPassword("dev-password-123")
		if err != nil {
			return 0, err
		}
		id, err := st.CreateUser(ctx, store.NewUser{Username: "admin", Email: "admin@example.org", DisplayName: "sap13ns team",
			Role: auth.RoleAdmin, PasswordHash: hash})
		if err != nil {
			return 0, err
		}
		fmt.Println("created user admin with password dev-password-123 (development only)")
		return id, nil
	}
	if u, err := st.UserByUsername(ctx, "admin"); err == nil && u.Role == auth.RoleAdmin {
		return u.ID, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	admins, _, err := st.ListUsers(ctx, store.UserListQuery{Role: auth.RoleAdmin, Limit: 1})
	if err != nil {
		return 0, err
	}
	if len(admins) == 0 {
		return 0, errors.New("no administrator found to author the content")
	}
	return admins[0].ID, nil
}

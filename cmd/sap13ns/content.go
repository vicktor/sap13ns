package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/contentpack"
)

const contentUsage = `usage:
  sap13ns content load [-env FILE] [-site S] [-author USERNAME] <dir>`

// contentCmd implements "sap13ns content load": a content pack (site.json
// and <lang>/*.md pages and posts, the format of internal/contentpack) is
// imported into a site, as scripts/sitepack does in development. It is how
// the sap13ns.site pages reach a server without Go: on a network, into the
// portal with -site network. Running it again updates what changed only.
func contentCmd(args []string) error {
	if len(args) == 0 || args[0] != "load" {
		return errors.New(contentUsage)
	}
	args = args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0]) // the directory may come before the flags
	}
	var siteSlug, authorName string
	cfg, log, fs, err := setup("content load", args, false, func(f *flag.FlagSet) {
		f.StringVar(&siteSlug, "site", "", "network: the site (network for the portal)")
		f.StringVar(&authorName, "author", "", "the user that signs new items (default: admin, or the oldest administrator)")
	})
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(contentUsage)
	}
	dir := fs.Arg(0)
	if fi, err := os.Stat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if cfg.Multisite && len(cfg.SecretKey) == 0 {
		return errors.New("SAP13NS_SECRET_KEY is required to open a site of the network")
	}
	t, err := openTarget(ctx, cfg, log, siteSlug, 4)
	if err != nil {
		return err
	}
	defer t.close()

	var authorID int64
	if authorName != "" {
		u, err := t.store.UserByUsername(ctx, strings.ToLower(authorName))
		if err != nil {
			return fmt.Errorf("user %s: %w", authorName, err)
		}
		if !auth.Can(u.Role, auth.CapEditOthersPosts) {
			return fmt.Errorf("user %s is a %s: the author must be an editor or an administrator", u.Username, u.Role)
		}
		authorID = u.ID
	} else if authorID, err = contentpack.DefaultAuthor(ctx, t.store); err != nil {
		return err
	}

	rep, err := contentpack.Load(ctx, t.store, dir, contentpack.Options{AuthorID: authorID, Now: time.Now()})
	if rep != nil {
		fmt.Fprint(out, rep.String())
	}
	if err != nil {
		return err
	}
	if err := t.store.Audit(ctx, 0, "content.loaded", filepath.Base(filepath.Clean(dir)), netipZero, map[string]string{
		"created": strconv.Itoa(rep.Created), "updated": strconv.Itoa(rep.Updated), "unchanged": strconv.Itoa(rep.Unchanged),
	}); err != nil {
		log.Error("audit the content load", "err", err)
	}
	if t.net != nil {
		if err := t.net.SiteRestored(ctx, t.site.ID); err != nil {
			fmt.Fprintln(os.Stderr, "warning: the running servers were not told; they pick the content up when the site reloads:", err)
		}
		return nil
	}
	fmt.Fprintln(out, "restart sap13ns (systemctl restart sap13ns) so that a running server picks up the settings and menus")
	return nil
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"sap13ns/internal/sitecheck"
	"sap13ns/internal/web"
)

// settingsCmd works on the settings of a site that cannot wait for the
// admin:
//
//	sap13ns settings login-path [-site S]          print the sign-in address
//	sap13ns settings login-path [-site S] -reset   put back /login
//	sap13ns settings login-path [-site S] -set /x  choose another one
//
// A running server picks a change up within a minute (it watches the
// settings' stamp), and drops the pages that linked to the old address.
func settingsCmd(args []string) error {
	if len(args) == 0 || args[0] != "login-path" {
		return errors.New("usage: sap13ns settings login-path [-site S] [-reset | -set /PATH]")
	}
	var site, set string
	var reset bool
	cfg, log, _, err := setup("settings login-path", args[1:], false, func(f *flag.FlagSet) {
		f.StringVar(&site, "site", "", "network: the site (network for the portal)")
		f.BoolVar(&reset, "reset", false, "put back the default address, "+sitecheck.DefaultLoginPath)
		f.StringVar(&set, "set", "", "choose this address, such as /entrar")
	})
	if err != nil {
		return err
	}
	if reset && set != "" {
		return errors.New("give -reset or -set, not both")
	}
	ctx := context.Background()
	t, err := openTarget(ctx, cfg, log, site, 2)
	if err != nil {
		return err
	}
	defer t.close()
	st, err := t.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	old := web.SignInPath(st)
	next := old
	switch {
	case reset:
		next = sitecheck.DefaultLoginPath
	case set != "":
		next = "/" + strings.Trim(strings.ToLower(strings.TrimSpace(set)), "/")
		if !sitecheck.LoginPath.MatchString(next) {
			return fmt.Errorf("%s: a sign-in address is one or two words of lowercase letters, numbers and dashes, of 3 to 41 characters each, such as /entrar", next)
		}
		free, err := web.SignInPathFree(ctx, t.store, next)
		if err != nil {
			return err
		}
		if !free {
			return fmt.Errorf("%s is taken: the site uses it for one of its own addresses, a page or a redirect", next)
		}
	}
	if next != st.LoginPath {
		st.LoginPath = next
		if err := t.store.SaveSettings(ctx, st); err != nil {
			return err
		}
		t.store.Audit(ctx, 0, "settings.login_path", next, netipZero, map[string]string{"from": old, "by": "cli"})
	}
	fmt.Fprintf(out, "sign-in address: %s\n%s\n", next, t.cfg.AbsURL(next))
	if next != old {
		fmt.Fprintf(out, "changed from %s: a running server uses the new address within a minute, and %s stops working\n", old, old)
	}
	hidden := "off"
	if st.HideAdmin {
		hidden = "on (visitors who open /admin get the page-not-found page)"
	}
	fmt.Fprintf(out, "hide the administration from visitors: %s\n", hidden)
	return nil
}

// signInLine is what user create and user reset-password print: where the
// user signs in.
func signInLine(ctx context.Context, t *target) string {
	st, err := t.store.LoadSettings(ctx)
	if err != nil {
		return ""
	}
	return "sign in at " + t.cfg.AbsURL(web.SignInPath(st)) + "\n"
}

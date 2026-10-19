package contentpack

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// Load imports the pack in dir: site.json, when there is one, and the
// <lang>/*.md items. The whole pack is parsed before the database is
// touched, so a broken pack changes nothing. Without site.json the current
// settings are kept, and the report says so.
func Load(ctx context.Context, st *store.Store, dir string, opts Options) (*Report, error) {
	items, err := Parse(dir)
	if err != nil {
		return nil, err
	}
	noSite := false
	if opts.Site == nil {
		site, err := LoadSite(filepath.Join(dir, "site.json"))
		switch {
		case err == nil:
			opts.Site = site
		case errors.Is(err, os.ErrNotExist):
			noSite = true
		default:
			return nil, err
		}
	}
	rep, err := Import(ctx, st, items, opts)
	if rep != nil && noSite {
		rep.Warnings = append(rep.Warnings, "site.json not found: the current settings were kept")
	}
	return rep, err
}

// DefaultAuthor returns the user that signs a pack's content: "admin" when
// it is an administrator, otherwise the oldest administrator.
func DefaultAuthor(ctx context.Context, st *store.Store) (int64, error) {
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
		return 0, errors.New("no administrator to author the content: create one first with `sap13ns user create -role admin`")
	}
	return admins[0].ID, nil
}

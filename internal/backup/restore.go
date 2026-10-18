package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// RestoreOptions configures Restore.
type RestoreOptions struct {
	DatabaseURL string
	UploadsDir  string
	Passphrase  string
	SkipDB      bool
	SkipUploads bool
	Force       bool   // restore over a database that already has tables
	PgRestore   string // default "pg_restore" from PATH
	TempDir     string // holds the verified dump for pg_restore; default os.TempDir()
	Now         func() time.Time
	Log         *slog.Logger

	probe probeFunc
}

func (o RestoreOptions) withDefaults() RestoreOptions {
	if o.PgRestore == "" {
		o.PgRestore = "pg_restore"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.probe == nil {
		o.probe = probeServer
	}
	return o
}

// Restore puts an archive back. It first verifies the whole archive. It
// refuses to touch a database that already has tables unless Force is set,
// then extracts the dump and the uploads to temporary locations, checking
// every hash again so that the file cannot be swapped between the two
// passes. Only then does it run pg_restore in a single transaction. Last, it
// moves the extracted uploads into place and keeps the previous directory as
// <UploadsDir>.bak-<timestamp>.
//
// Stop the site while restoring. pg_restore --clean drops only the objects
// that the dump contains.
func Restore(ctx context.Context, archivePath string, opts RestoreOptions) error {
	o := opts.withDefaults()
	switch {
	case o.SkipDB && o.SkipUploads:
		return errors.New("restore: nothing to do with both the database and the uploads skipped")
	case !o.SkipDB && o.DatabaseURL == "":
		return errors.New("restore: database url is required")
	case !o.SkipUploads && o.UploadsDir == "":
		return errors.New("restore: uploads directory is required")
	}

	m, digest, err := walk(archivePath, o.Passphrase, nil)
	if err != nil {
		return err
	}
	o.Log.Info("archive verified", "path", archivePath, "created", m.Created, "app_version", m.AppVersion,
		"postgres_version", m.PGVersion, "files", m.FileCount)

	var env []string
	var dbname string
	var dumpFile *os.File
	if !o.SkipDB {
		if env, dbname, err = pgEnv(o.DatabaseURL); err != nil {
			return err
		}
		srv, err := o.probe(ctx, o.DatabaseURL)
		if err != nil {
			return err
		}
		if srv.Relations > 0 {
			if !o.Force {
				return fmt.Errorf("%w: %q has %d tables, sequences or views; restore with force to replace them", ErrNotEmpty, dbname, srv.Relations)
			}
			o.Log.Warn("restoring over a database that is not empty", "database", dbname, "relations", srv.Relations)
		}
		if dumpFile, err = os.CreateTemp(o.TempDir, "sap13ns-restore-*.dump"); err != nil {
			return err
		}
		defer func() {
			dumpFile.Close()
			os.Remove(dumpFile.Name())
		}()
	}

	var x *extractor
	var uploadsDir string
	if !o.SkipUploads {
		if uploadsDir, err = filepath.Abs(o.UploadsDir); err != nil {
			return err
		}
		if fi, err := os.Lstat(uploadsDir); err == nil && !fi.IsDir() {
			return fmt.Errorf("restore: %s is not a directory (a symlink? restore into its target)", uploadsDir)
		}
		parent := filepath.Dir(uploadsDir)
		if err := os.MkdirAll(parent, 0o750); err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(parent, "."+filepath.Base(uploadsDir)+".restore-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp) // gone after a successful swap
		if x, err = newExtractor(tmp); err != nil {
			return err
		}
		defer x.close()
	}

	_, digest2, err := walk(archivePath, o.Passphrase, func(e Member, h *tar.Header, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Name == dumpName {
			if dumpFile == nil {
				return nil
			}
			_, err := io.Copy(dumpFile, r)
			return err
		}
		if x == nil {
			return nil
		}
		return x.file(strings.TrimPrefix(e.Name, uploadsPrefix), h.ModTime, r)
	})
	if err != nil {
		return err
	}
	if digest2 != digest {
		return fmt.Errorf("%w: archive changed while it was being restored", ErrCorrupt)
	}

	if dumpFile != nil {
		if err := dumpFile.Close(); err != nil {
			return err
		}
		args := []string{"--clean", "--if-exists", "--no-owner", "--no-privileges", "--single-transaction",
			"--no-password", conninfoDB(dbname), dumpFile.Name()}
		if err := runTool(ctx, o.Log, o.PgRestore, args, env, nil); err != nil {
			return err
		}
		o.Log.Info("database restored", "database", dbname)
	}

	if x != nil {
		x.close()
		bak, err := swapDir(x.dir, uploadsDir, o.Now().UTC().Format(stampLayout))
		if err != nil {
			return fmt.Errorf("restore uploads: %w", err)
		}
		o.Log.Info("uploads restored", "dir", uploadsDir, "files", m.FileCount, "previous", bak)
	}
	return nil
}

// extractor writes upload members below a fresh directory. Names have been
// checked already; os.Root guarantees again that nothing lands outside it.
type extractor struct {
	dir  string
	root *os.Root
	dirs map[string]bool
}

func newExtractor(dir string) (*extractor, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &extractor{dir: dir, root: root, dirs: map[string]bool{}}, nil
}

func (x *extractor) close() { x.root.Close() }

// file creates rel with the content of r. Modes are fixed, as the media
// package writes them (0644 files, 0755 directories); the archive's modes
// are ignored.
func (x *extractor) file(rel string, mtime time.Time, r io.Reader) error {
	if !safeName(rel) {
		return fmt.Errorf("%w: unsafe path %q", ErrUnsafeMember, rel)
	}
	if dir := path.Dir(rel); dir != "." {
		if err := x.mkdirs(dir); err != nil {
			return err
		}
	}
	f, err := x.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return x.root.Chtimes(rel, mtime, mtime)
}

func (x *extractor) mkdirs(dir string) error {
	if x.dirs[dir] {
		return nil
	}
	if parent := path.Dir(dir); parent != "." && parent != dir {
		if err := x.mkdirs(parent); err != nil {
			return err
		}
	}
	if err := x.root.Mkdir(dir, 0o755); err != nil {
		// Only a directory made by an earlier member may already exist,
		// for example on a case-insensitive file system.
		if fi, serr := x.root.Lstat(dir); !errors.Is(err, fs.ErrExist) || serr != nil || !fi.IsDir() {
			return err
		}
	}
	if err := x.root.Chmod(dir, 0o755); err != nil {
		return err
	}
	x.dirs[dir] = true
	return nil
}

// swapDir moves dir to target, keeping a previous target as
// target.bak-<stamp>. Both steps are atomic renames, and if the second one
// fails the first is undone.
func swapDir(dir, target, stamp string) (backup string, err error) {
	fi, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Chmod(dir, 0o750); err != nil {
			return "", err
		}
		if err := os.Rename(dir, target); err != nil {
			return "", err
		}
		return "", syncDir(filepath.Dir(target))
	case err != nil:
		return "", err
	case !fi.IsDir():
		return "", fmt.Errorf("%s is not a directory", target)
	}
	if err := os.Chmod(dir, fi.Mode().Perm()); err != nil {
		return "", err
	}
	backup = target + ".bak-" + stamp
	if _, err := os.Lstat(backup); err == nil {
		return "", fmt.Errorf("%s already exists", backup)
	}
	if err := os.Rename(target, backup); err != nil {
		return "", err
	}
	if err := os.Rename(dir, target); err != nil {
		if rerr := os.Rename(backup, target); rerr != nil {
			return "", fmt.Errorf("%w; the previous uploads are now in %s (moving them back failed: %v)", err, backup, rerr)
		}
		return "", err
	}
	return backup, syncDir(filepath.Dir(target))
}

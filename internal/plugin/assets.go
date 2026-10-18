package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Static files a plugin may ship under assets/, with the content type they
// are served with.
var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2", ".json": "application/json", ".txt": "text/plain; charset=utf-8",
}

const (
	maxAssetBytes  = 2 << 20
	maxAssetsTotal = 8 << 20
	maxAssetFiles  = 100
)

type assetFile struct {
	data        []byte
	contentType string
}

// readAssets loads assets/ and returns its files with a hash over their
// paths and contents ("" when there is no assets directory). Symbolic links
// and unknown file types are errors.
func readAssets(dir string) (map[string]assetFile, string, error) {
	root := filepath.Join(dir, "assets")
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !fi.IsDir() {
		return nil, "", errors.New("assets must be a directory")
	}
	files := map[string]assetFile{}
	total := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symbolic links are not allowed", rel)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: not a regular file", rel)
		}
		ctype, ok := assetTypes[strings.ToLower(path.Ext(rel))]
		if !ok {
			return fmt.Errorf("%s: file type not allowed in assets/", rel)
		}
		if len(files) >= maxAssetFiles {
			return errors.New("too many files in assets/")
		}
		b, err := readLimited(p, maxAssetBytes)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if total += len(b); total > maxAssetsTotal {
			return errors.New("assets/ is too large")
		}
		files[rel] = assetFile{data: b, contentType: ctype}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return files, "", nil
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		sum := sha256.Sum256(files[n].data)
		fmt.Fprintf(h, "%s\x00%x\n", n, sum)
	}
	return files, hex.EncodeToString(h.Sum(nil)), nil
}

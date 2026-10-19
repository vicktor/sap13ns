package export

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/media"
	"sap13ns/internal/store"
)

// The members of a .zip export:
//
//	export.xml               the WXR file
//	posts/<lang>/<slug>.md   each post: YAML front matter and its Markdown
//	pages/<lang>/<slug>.md   each page
//	uploads/...              the uploads directory (with Options.Media)
const (
	ZipWXR     = "export.xml"
	zipUploads = "uploads/"
)

// WriteZip writes the .zip export of o to w. The WXR file, the Markdown
// copies and the files are streamed into it one after the other.
func WriteZip(ctx context.Context, st *store.Store, w io.Writer, o Options) (*Report, error) {
	e := newExporter(st, o)
	bw := bufio.NewWriterSize(w, 256<<10)
	z := &zipper{zw: zip.NewWriter(bw), budget: e.o.MaxBytes, now: e.now}
	rep, err := e.zip(ctx, z)
	if err != nil {
		return rep, err
	}
	if err := z.zw.Close(); err != nil {
		return rep, err
	}
	return rep, bw.Flush()
}

func (e *exporter) zip(ctx context.Context, z *zipper) (*Report, error) {
	fw, err := z.create(ZipWXR, true, e.now)
	if err != nil {
		return e.rep, err
	}
	lw := &limitWriter{w: fw, remaining: e.o.MaxXMLBytes}
	x := &xmlWriter{w: bufio.NewWriterSize(lw, 64<<10)}
	if err := e.wxr(ctx, x); err != nil {
		return e.rep, err
	}
	if err := x.flush(); err != nil {
		return e.rep, err
	}
	e.rep.Bytes = lw.n
	if err := z.charge(lw.n); err != nil {
		return e.rep, err
	}

	err = e.eachPost(ctx, false, func(p *store.Post, _ int) error {
		dir := "posts/"
		if p.Type == store.TypePage {
			dir = "pages/"
		}
		var buf bytes.Buffer
		e.markdown(&buf, p)
		if err := z.charge(int64(buf.Len())); err != nil {
			return err
		}
		fw, err := z.create(dir+p.Lang+"/"+p.Slug+".md", true, p.UpdatedAt)
		if err != nil {
			return err
		}
		_, err = buf.WriteTo(fw)
		return err
	})
	if err != nil {
		return e.rep, err
	}
	if e.o.Media && e.o.Site.UploadsDir != "" {
		if err := e.zipUploads(ctx, z); err != nil {
			return e.rep, err
		}
	}
	return e.rep, nil
}

// markdown writes a post as a Markdown file with YAML front matter. Values
// are JSON strings and arrays, which YAML reads as they are.
func (e *exporter) markdown(w *bytes.Buffer, p *store.Post) {
	str := func(v string) string {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.Encode(v)
		return strings.TrimSuffix(b.String(), "\n")
	}
	list := func(vs []string) string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = str(v)
		}
		return "[" + strings.Join(out, ", ") + "]"
	}
	field := func(k, v string) { fmt.Fprintf(w, "%s: %s\n", k, v) }
	stamp := func(t time.Time) string { return str(t.UTC().Format(time.RFC3339)) }

	w.WriteString("---\n")
	field("id", fmt.Sprint(p.ID))
	field("type", str(p.Type))
	field("title", str(p.Title))
	field("slug", str(p.Slug))
	field("lang", str(p.Lang))
	field("status", str(p.Status))
	if p.TGroup != nil {
		field("translation_group", fmt.Sprint(*p.TGroup))
	}
	if p.PublishedAt != nil {
		field("date", stamp(*p.PublishedAt))
	}
	field("created", stamp(p.CreatedAt))
	field("updated", stamp(p.UpdatedAt))
	if p.AuthorUsername != "" {
		field("author", str(p.AuthorUsername))
	}
	if p.ParentID != nil {
		field("parent", fmt.Sprint(*p.ParentID))
	}
	if p.Type == store.TypePage {
		field("menu_order", fmt.Sprint(p.MenuOrder))
	}
	var cats, tags []string
	for _, t := range p.Terms {
		if t.Taxonomy == "tag" {
			tags = append(tags, t.Slug)
		} else {
			cats = append(cats, t.Slug)
		}
	}
	if len(cats) > 0 {
		field("categories", list(cats))
	}
	if len(tags) > 0 {
		field("tags", list(tags))
	}
	if p.Excerpt != "" {
		field("excerpt", str(p.Excerpt))
	}
	if p.MetaDescription != "" {
		field("description", str(p.MetaDescription))
	}
	if p.Featured != nil {
		field("featured_image", str(p.Featured.URL()))
	}
	field("comments", str(p.CommentStatus))
	w.WriteString("---\n\n")
	w.WriteString(strings.ToValidUTF8(p.Content, "�"))
	if !strings.HasSuffix(p.Content, "\n") {
		w.WriteString("\n")
	}
}

// zipUploads adds the regular files of the uploads directory. Symlinks,
// special files, the importer's temporary files and names that could not
// be extracted safely are left out, as backups do, and so are the photos
// of users and the Gravatar copies (media.AvatarDir): they are not
// content, and nothing in the export names them.
func (e *exporter) zipUploads(ctx context.Context, z *zipper) error {
	root, err := os.OpenRoot(e.o.Site.UploadsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("uploads: %w", err)
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed meanwhile
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := d.Type(); {
		case rel == ".":
			return nil
		case t.IsDir():
			if rel == ".tmp" || rel == media.AvatarDir {
				return fs.SkipDir
			}
			return nil
		case !t.IsRegular(), strings.HasSuffix(rel, ".tmp"), !safeName(zipUploads + rel):
			return nil
		}
		if e.rep.Files >= e.o.MaxFiles {
			return fmt.Errorf("%w: more than %d files", ErrTooLarge, e.o.MaxFiles)
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := z.charge(info.Size()); err != nil {
			return err
		}
		f, err := root.Open(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		fw, err := z.create(zipUploads+rel, compressible(rel), info.ModTime())
		if err != nil {
			return err
		}
		// Exactly the size charged: a file that grew meanwhile is cut.
		if _, err := io.Copy(fw, &ctxReader{ctx: ctx, r: io.LimitReader(f, info.Size())}); err != nil {
			return err
		}
		e.rep.Files++
		return nil
	})
}

// compressible reports whether deflating a file is worth it: formats that
// are compressed already are stored.
func compressible(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic", ".mp4", ".m4v", ".webm", ".mov",
		".mp3", ".m4a", ".ogg", ".oga", ".opus", ".flac", ".zip", ".gz", ".woff", ".woff2":
		return false
	}
	return true
}

// safeName reports whether name is a canonical, relative, slash-separated
// path that extracts inside its directory, as backups require.
func safeName(name string) bool {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, el := range strings.Split(name, "/") {
		if el == ".." || el == "." {
			return false
		}
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' })
}

type zipper struct {
	zw     *zip.Writer
	budget int64 // bytes left before compression
	now    time.Time
}

func (z *zipper) create(name string, deflate bool, mod time.Time) (io.Writer, error) {
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: mod}
	if deflate {
		h.Method = zip.Deflate
	}
	h.SetMode(0o644)
	return z.zw.CreateHeader(h)
}

func (z *zipper) charge(n int64) error {
	if n > z.budget {
		return ErrTooLarge
	}
	z.budget -= n
	return nil
}

// ctxReader stops a long copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

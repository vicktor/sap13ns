package themeimport

import (
	"archive/zip"
	"io"
	"path"
	"strings"
)

// Kind is the kind of theme an archive holds.
type Kind string

const (
	KindSap13ns          Kind = "sap13ns"           // a theme of this CMS: install it as it is
	KindWordPressBlock   Kind = "wordpress-block"   // converted by Convert
	KindWordPressClassic Kind = "wordpress-classic" // PHP templates: not supported
	KindGhost            Kind = "ghost"             // Handlebars: not supported yet
	KindHugo             Kind = "hugo"              // Go templates of Hugo: not supported yet
	KindJekyll           Kind = "jekyll"            // Liquid: not supported
	KindUnknown          Kind = ""
)

// Convertible reports whether Convert reads this kind.
func (k Kind) Convertible() bool { return k == KindWordPressBlock }

// Detect tells the kind of theme of an archive from the names of its
// files alone: nothing is unpacked. An archive that is not a readable
// .zip within the importer's bounds is KindUnknown, and the caller's
// own checks give the reason.
func Detect(r io.ReaderAt, size int64) Kind {
	if size <= 0 || size > MaxArchive {
		return KindUnknown
	}
	zr, err := zip.NewReader(r, size)
	if err != nil || len(zr.File) > maxEntries {
		return KindUnknown
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		n := strings.TrimPrefix(f.Name, "./")
		if strings.HasPrefix(n, "__MACOSX/") || n == "" {
			continue
		}
		names = append(names, n)
	}
	return detectNames(names)
}

func detectNames(names []string) Kind {
	// The package may sit in one top-level folder.
	if len(names) > 0 {
		top, _, _ := strings.Cut(names[0], "/")
		inside := top != names[0] || strings.HasSuffix(names[0], "/")
		for _, n := range names {
			if n != top+"/" && !strings.HasPrefix(n, top+"/") {
				inside = false
				break
			}
		}
		if inside {
			for i, n := range names {
				names[i] = strings.TrimPrefix(n, top+"/")
			}
		}
	}
	has := map[string]bool{}
	dirs := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSuffix(n, "/")
		has[n] = true
		for d := path.Dir(n); d != "." && d != "/"; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	anyIn := func(dir, ext string) bool {
		for n := range has {
			if strings.HasPrefix(n, dir+"/") && strings.HasSuffix(n, ext) {
				return true
			}
		}
		return false
	}
	switch {
	case has["theme.json"] && has["templates/layout.html"]:
		return KindSap13ns
	case has["style.css"] && (has["templates/index.html"] || has["block-templates/index.html"]):
		return KindWordPressBlock
	case has["style.css"] && has["index.php"]:
		return KindWordPressClassic
	case has["package.json"] && (has["index.hbs"] || has["post.hbs"] || has["default.hbs"]):
		return KindGhost
	case dirs["layouts"] && (has["theme.toml"] || has["hugo.toml"] || has["config.toml"] || has["theme.yaml"] ||
		dirs["layouts/_default"] || has["layouts/baseof.html"] || has["layouts/index.html"]):
		return KindHugo
	case dirs["_layouts"] && (has["_config.yml"] || anyIn("_layouts", ".html") || anyGemspec(has)):
		return KindJekyll
	}
	return KindUnknown
}

func anyGemspec(has map[string]bool) bool {
	for n := range has {
		if !strings.Contains(n, "/") && strings.HasSuffix(n, ".gemspec") {
			return true
		}
	}
	return false
}

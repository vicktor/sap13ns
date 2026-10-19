package content

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Per-call options reach the (shared) transformers through the parser
// context.
var (
	videoOptionsKey = parser.NewContextKey()
	imageOptionsKey = parser.NewContextKey()
)

// ------------------------------------------------------------------ videos --

var kindVideo = ast.NewNodeKind("Video")

// videoBlock is a paragraph that named a video, already rendered.
type videoBlock struct {
	ast.BaseBlock
	html string
}

func (n *videoBlock) Kind() ast.NodeKind            { return kindVideo }
func (n *videoBlock) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// videos turns top-level paragraphs that hold nothing but a YouTube or Vimeo
// URL, or the [video] shortcode, into players (see VideoOptions). It only
// looks at the document's own paragraphs: a URL in a list, a quote or next to
// other text stays a link, as in WordPress.
type videos struct{}

func (videos) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	o, _ := pc.Get(videoOptionsKey).(*VideoOptions)
	src := reader.Source()
	untitled := map[string]int{}
	for c := doc.FirstChild(); c != nil; {
		next := c.NextSibling()
		p, ok := c.(*ast.Paragraph)
		if !ok || p.Lines().Len() != 1 {
			c = next
			continue
		}
		seg := p.Lines().At(0)
		v, shortcode, ok := videoLine(string(seg.Value(src)))
		// Without options a bare URL stays the link Markdown makes of it;
		// the shortcode always becomes something readable.
		if !ok || (o == nil && !shortcode) {
			c = next
			continue
		}
		var meta VideoMeta
		if o != nil && o.Meta != nil && o.Mode != VideoWordPress {
			meta = o.Meta(v)
		}
		if o != nil && o.Found != nil {
			o.Found(v)
		}
		n := 0
		if v.Title == "" && meta.Title == "" {
			untitled[v.Provider]++
			n = untitled[v.Provider]
		}
		doc.ReplaceChild(doc, c, &videoBlock{html: videoHTML(v, meta, o, n)})
		c = next
	}
}

// videoRenderer writes the HTML of a videoBlock.
type videoRenderer struct{}

func (videoRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindVideo, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString(n.(*videoBlock).html)
		}
		return ast.WalkSkipChildren, nil
	})
}

// ------------------------------------------------------------------ images --

// ImageSource is one file of an image: the original or a resized variant.
type ImageSource struct {
	URL   string
	Width int
}

// ImageInfo describes an image of the media library.
type ImageInfo struct {
	Width, Height int           // the original's size
	Sources       []ImageSource // every file, narrowest first
}

// ImageAligns are the alignments of ![alt](src){align=…}: left and right
// float with the text around the image, center and full do not.
var ImageAligns = []string{"left", "right", "center", "full"}

// MaxImageWidth bounds {width=…}.
const MaxImageWidth = 4096

var imageAttrBlock = regexp.MustCompile(`^\{([a-z]{1,10}=[a-z0-9]{1,10}(?: [a-z]{1,10}=[a-z0-9]{1,10}){0,3})\}`)

// ImageAttrs parses the inside of {width=480 align=left}. ok is false when
// anything in it is unknown, out of range or repeated.
func ImageAttrs(s string) (width int, align string, ok bool) {
	seen := map[string]bool{}
	for _, part := range strings.Fields(s) {
		k, v, _ := strings.Cut(part, "=")
		if seen[k] {
			return 0, "", false
		}
		seen[k] = true
		switch k {
		case "width":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > MaxImageWidth || strconv.Itoa(n) != v {
				return 0, "", false
			}
			width = n
		case "align":
			if !validAlign(v) {
				return 0, "", false
			}
			align = v
		default:
			return 0, "", false
		}
	}
	return width, align, len(seen) > 0
}

func validAlign(a string) bool {
	for _, x := range ImageAligns {
		if a == x {
			return true
		}
	}
	return false
}

// imageAttrs reads {width=480 align=left} right after an image (goldmark's
// attribute extension only covers headings) and turns it into the width,
// height and class (align-left …) of the <img>. With media-library data
// (ImageInfo) the image also gets its height from the original's aspect
// ratio and a srcset of its variants.
type imageAttrs struct{}

func (imageAttrs) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	lookup, _ := pc.Get(imageOptionsKey).(func(string) (ImageInfo, bool))
	src := reader.Source()
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			images = append(images, img)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		first, ok := img.NextSibling().(*ast.Text)
		if !ok {
			continue
		}
		start := first.Segment.Start
		end := start + bytesIndexByte(src[start:], '\n')
		m := imageAttrBlock.FindSubmatch(src[start:end])
		if m == nil {
			continue
		}
		width, align, ok := ImageAttrs(string(m[1]))
		if !ok || !consumeText(img, start+len(m[0])) {
			continue
		}
		applyImageAttrs(img, width, align, lookup)
	}
}

func bytesIndexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return len(b)
}

// consumeText removes the source up to end from the text nodes after img,
// which must all be plain, contiguous text up to there.
func consumeText(img *ast.Image, end int) bool {
	var nodes []*ast.Text
	pos := -1
	for n := img.NextSibling(); n != nil && pos < end; n = n.NextSibling() {
		t, ok := n.(*ast.Text)
		if !ok || (pos >= 0 && t.Segment.Start != pos) {
			return false
		}
		nodes = append(nodes, t)
		pos = t.Segment.Stop
	}
	if pos < end {
		return false
	}
	parent := img.Parent()
	for _, t := range nodes {
		switch {
		case t.Segment.Stop > end:
			t.Segment = t.Segment.WithStart(end)
		case t.SoftLineBreak() || t.HardLineBreak():
			t.Segment = t.Segment.WithStart(t.Segment.Stop) // keep the line break
		default:
			parent.RemoveChild(parent, t)
		}
	}
	return true
}

func applyImageAttrs(img *ast.Image, width int, align string, lookup func(string) (ImageInfo, bool)) {
	if align != "" {
		img.SetAttributeString("class", []byte("align-"+align))
	}
	if width == 0 {
		return
	}
	img.SetAttributeString("width", []byte(strconv.Itoa(width)))
	if lookup == nil {
		return
	}
	info, ok := lookup(string(img.Destination))
	if !ok || info.Width <= 0 || info.Height <= 0 {
		return
	}
	h := (width*info.Height + info.Width/2) / info.Width
	if h >= 1 && h <= MaxImageWidth*4 {
		img.SetAttributeString("height", []byte(strconv.Itoa(h)))
	}
	if len(info.Sources) < 2 {
		return
	}
	var set []string
	pick := ""
	for _, s := range info.Sources {
		if s.Width <= 0 || !uploadURL.MatchString(s.URL) {
			return
		}
		set = append(set, s.URL+" "+strconv.Itoa(s.Width)+"w")
		if pick == "" && s.Width >= width {
			pick = s.URL
		}
	}
	if pick == "" {
		pick = info.Sources[len(info.Sources)-1].URL
	}
	img.Destination = []byte(pick)
	img.SetAttributeString("srcset", []byte(strings.Join(set, ", ")))
	img.SetAttributeString("sizes", []byte("(max-width: 640px) 100vw, "+strconv.Itoa(width)+"px"))
}

var uploadURL = regexp.MustCompile(`^/uploads/[A-Za-z0-9/_.-]{1,100}$`)

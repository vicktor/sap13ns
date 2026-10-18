package builder

// Field is one setting of a block type. Every value is stored as a string
// and checked against its field: free text is length-capped, enumerations
// only take their listed values, links must be safe and media must exist.
type Field struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"` // text | textarea | markdown | select | bool | int | url | email | image | menu | area
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	Default string   `json:"default,omitempty"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"` // characters, or the largest int
	I18n    bool     `json:"i18n,omitempty"`
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Type is a block type. Where lists the regions it may be placed in ("*"
// for every region); Once means at most one per region.
type Type struct {
	Type   string   `json:"type"`
	Label  string   `json:"label"`
	Icon   string   `json:"icon"`
	Help   string   `json:"help,omitempty"`
	Where  []string `json:"where"`
	Once   bool     `json:"once,omitempty"`
	Fields []Field  `json:"fields"`
}

// Regions: the global header and footer, and one main area per page type.
var (
	PageKinds = []string{"home", "single", "page", "archive", "search", "404"}
	Regions   = append([]string{"header", "footer"}, PageKinds...)
)

// Column presets of a section (relative widths).
var Presets = map[string]int{"1": 1, "1-1": 2, "2-1": 2, "1-2": 2, "1-1-1": 3}

// PresetOrder is the order presets are offered in.
var PresetOrder = []string{"1", "1-1", "2-1", "1-2", "1-1-1"}

// Section appearance: each key maps to an enumerated class.
var SectionStyles = map[string][]string{
	"background": {"none", "muted", "card", "primary", "accent", "inverse"},
	"padding":    {"medium", "none", "small", "large"},
	"width":      {"content", "narrow", "wide", "full"},
	"align":      {"start", "center"},
}

// StyleOrder is the order section style fields are shown in.
var StyleOrder = []string{"background", "padding", "width", "align"}

func choices(pairs ...string) []Choice {
	out := make([]Choice, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Choice{Value: pairs[i], Label: pairs[i+1]})
	}
	return out
}

var (
	alignLC   = choices("left", "Left", "center", "Centre")
	alignLCR  = choices("left", "Left", "center", "Centre", "right", "Right")
	sizesSML  = choices("small", "Small", "medium", "Medium", "large", "Large")
	onOff     = func(key, label, def string) Field { return Field{Key: key, Type: "bool", Label: label, Default: def} }
	everwhere = []string{"*"}
	listing   = []string{"home", "archive", "search"}
	content   = []string{"single", "page"}
)

// Types are the block types, in the order the editor offers them.
var Types = []Type{
	{Type: "site-header", Label: "Site header", Icon: "panel-top", Where: []string{"header"}, Once: true,
		Help: "Brand, main menu, search, language switch and the light/dark toggle.",
		Fields: []Field{
			{Key: "menu", Type: "menu", Label: "Menu", Default: "primary"},
			{Key: "layout", Type: "select", Label: "Arrangement", Default: "split", Choices: choices("split", "Brand left, links right", "center", "Brand centred above the menu")},
			onOff("tagline", "Show the tagline", "false"),
			onOff("search", "Search link", "true"),
			onOff("lang", "Language switch", "true"),
			onOff("toggle", "Light/dark toggle", "true"),
			onOff("sticky", "Stay at the top when scrolling", "false"),
		}},
	{Type: "menu", Label: "Menu", Icon: "list", Where: everwhere,
		Fields: []Field{
			{Key: "menu", Type: "menu", Label: "Menu", Default: "primary"},
			{Key: "title", Type: "text", Label: "Heading", Max: 80, I18n: true},
			{Key: "direction", Type: "select", Label: "Direction", Default: "horizontal", Choices: choices("horizontal", "Horizontal", "vertical", "Vertical")},
			{Key: "align", Type: "select", Label: "Alignment", Default: "left", Choices: alignLCR},
		}},
	{Type: "search", Label: "Search", Icon: "search", Where: everwhere,
		Fields: []Field{
			{Key: "placeholder", Type: "text", Label: "Placeholder", Max: 80, I18n: true},
		}},
	{Type: "breadcrumbs", Label: "Breadcrumbs", Icon: "chevrons-right", Where: []string{"single", "page", "archive", "search", "404"}, Once: true,
		Fields: []Field{}},
	{Type: "hero", Label: "Hero", Icon: "presentation", Where: everwhere,
		Help: "A large introduction. Empty texts use the site title and tagline.",
		Fields: []Field{
			{Key: "title", Type: "text", Label: "Title", Max: 160, I18n: true},
			{Key: "text", Type: "textarea", Label: "Text", Max: 500, I18n: true},
			{Key: "button_label", Type: "text", Label: "Button label", Max: 60, I18n: true},
			{Key: "button_url", Type: "url", Label: "Button link"},
			{Key: "image", Type: "image", Label: "Image"},
			{Key: "align", Type: "select", Label: "Alignment", Default: "center", Choices: alignLC},
			{Key: "size", Type: "select", Label: "Size", Default: "medium", Choices: sizesSML},
		}},
	{Type: "post-list", Label: "Post list", Icon: "layout-grid", Where: listing, Once: true,
		Help: "The posts of this page, with its pagination.",
		Fields: []Field{
			{Key: "layout", Type: "select", Label: "Layout", Default: "grid", Choices: choices("grid", "Grid", "list", "List", "bento", "Bento mosaic")},
			{Key: "title", Type: "text", Label: "Heading", Max: 120, I18n: true, Help: "Archives and searches show their own title when this is empty."},
			onOff("pagination", "Pagination", "true"),
		}},
	{Type: "single-content", Label: "Post content", Icon: "file-text", Where: content, Once: true,
		Help: "Title, summary and body of the post or page. Required on these pages.",
		Fields: []Field{
			onOff("title", "Title", "true"),
			onOff("summary", "Summary", "false"),
			{Key: "width", Type: "select", Label: "Text width", Default: "normal", Choices: choices("narrow", "Narrow", "normal", "Normal", "wide", "Wide")},
		}},
	{Type: "post-meta", Label: "Post details", Icon: "info", Where: content,
		Fields: []Field{
			onOff("date", "Date, author and reading time", "true"),
			onOff("categories", "Categories", "true"),
			onOff("tags", "Tags", "true"),
			onOff("share", "Share links", "false"),
		}},
	{Type: "featured-image", Label: "Featured image", Icon: "image", Where: content, Once: true,
		Fields: []Field{
			{Key: "ratio", Type: "select", Label: "Shape", Default: "auto", Choices: choices("auto", "Original", "wide", "Wide", "square", "Square")},
			onOff("caption", "Caption", "true"),
		}},
	{Type: "comments", Label: "Comments", Icon: "message-square", Where: content, Once: true, Fields: []Field{}},
	{Type: "pagination", Label: "Pagination", Icon: "move-vertical", Where: []string{"home", "archive", "search", "single"}, Once: true,
		Help: "Page numbers on listings; previous and next post on single posts.", Fields: []Field{}},
	{Type: "widget-area", Label: "Widget area", Icon: "layout-dashboard", Where: everwhere,
		Help: "The widgets of an area, as set in Appearance → Widgets.",
		Fields: []Field{
			{Key: "area", Type: "area", Label: "Area", Default: "sidebar"},
		}},
	{Type: "rich-text", Label: "Text", Icon: "pilcrow", Where: everwhere,
		Fields: []Field{
			{Key: "text", Type: "markdown", Label: "Text", Max: 20000, I18n: true, Help: "Markdown: **bold**, _italic_, [links](https://…), lists and headings."},
			{Key: "align", Type: "select", Label: "Alignment", Default: "left", Choices: alignLC},
			{Key: "size", Type: "select", Label: "Text size", Default: "normal", Choices: choices("small", "Small", "normal", "Normal", "large", "Large")},
		}},
	{Type: "image", Label: "Image", Icon: "image-plus", Where: everwhere,
		Fields: []Field{
			{Key: "image", Type: "image", Label: "Image"},
			{Key: "alt", Type: "text", Label: "Alternative text", Max: 250, I18n: true, Help: "Describe the image for people who cannot see it. Empty uses the media library's text."},
			{Key: "caption", Type: "text", Label: "Caption", Max: 300, I18n: true},
			{Key: "link", Type: "url", Label: "Link"},
			{Key: "size", Type: "select", Label: "Size", Default: "full", Choices: choices("small", "Small", "medium", "Medium", "full", "Full width")},
			{Key: "align", Type: "select", Label: "Alignment", Default: "center", Choices: alignLCR},
		}},
	{Type: "button", Label: "Button", Icon: "mouse-pointer-click", Where: everwhere,
		Fields: []Field{
			{Key: "label", Type: "text", Label: "Label", Max: 60, I18n: true, Default: "Learn more"},
			{Key: "url", Type: "url", Label: "Link", Default: "/"},
			{Key: "variant", Type: "select", Label: "Style", Default: "primary", Choices: choices("primary", "Primary", "secondary", "Secondary", "outline", "Outline", "link", "Link")},
			{Key: "size", Type: "select", Label: "Size", Default: "medium", Choices: sizesSML},
			{Key: "align", Type: "select", Label: "Alignment", Default: "left", Choices: alignLCR},
			onOff("new_tab", "Open in a new tab", "false"),
		}},
	{Type: "spacer", Label: "Spacer or divider", Icon: "separator-horizontal", Where: everwhere,
		Fields: []Field{
			{Key: "size", Type: "select", Label: "Height", Default: "medium", Choices: choices("small", "Small", "medium", "Medium", "large", "Large", "xlarge", "Extra large")},
			onOff("line", "Draw a line", "false"),
		}},
	{Type: "social-links", Label: "Social links", Icon: "share-2", Where: everwhere,
		Fields: []Field{
			{Key: "title", Type: "text", Label: "Heading", Max: 80, I18n: true},
			{Key: "mastodon", Type: "url", Label: "Mastodon"},
			{Key: "bluesky", Type: "url", Label: "Bluesky"},
			{Key: "github", Type: "url", Label: "GitHub"},
			{Key: "linkedin", Type: "url", Label: "LinkedIn"},
			{Key: "instagram", Type: "url", Label: "Instagram"},
			{Key: "youtube", Type: "url", Label: "YouTube"},
			{Key: "x", Type: "url", Label: "X"},
			{Key: "website", Type: "url", Label: "Website"},
			{Key: "email", Type: "email", Label: "E-mail"},
			onOff("rss", "RSS feed", "true"),
			{Key: "align", Type: "select", Label: "Alignment", Default: "left", Choices: alignLCR},
		}},
	{Type: "footer-meta", Label: "Footer line and credits", Icon: "badge-check", Where: []string{"footer"}, Once: true,
		Help: "Copyright, feed and the sap13ns credit the licence requires. Always shown in the footer.", Fields: []Field{}},
}

// SocialKeys are the social-links fields that hold profile links, in order.
var SocialKeys = []string{"mastodon", "bluesky", "github", "linkedin", "instagram", "youtube", "x", "website", "email"}

var typeIndex = func() map[string]*Type {
	m := map[string]*Type{}
	for i := range Types {
		m[Types[i].Type] = &Types[i]
	}
	return m
}()

// TypeOf returns the block type named t.
func TypeOf(t string) (*Type, bool) {
	ty, ok := typeIndex[t]
	return ty, ok
}

// Allowed reports whether blocks of type t may be placed in region.
func (t *Type) Allowed(region string) bool {
	for _, w := range t.Where {
		if w == "*" || w == region {
			return true
		}
	}
	return false
}

// Field returns the field named key.
func (t *Type) Field(key string) (*Field, bool) {
	for i := range t.Fields {
		if t.Fields[i].Key == key {
			return &t.Fields[i], true
		}
	}
	return nil, false
}

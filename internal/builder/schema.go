package builder

import "sap13ns/internal/motion"

// Field is one setting of a block type. Every value is stored as a string
// and checked against its field: free text is length-capped, enumerations
// only take their listed values, links must be safe and media must exist.
type Field struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"` // text | textarea | markdown | html | class | select | icon | bool | int | url | email | image | menu | area
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Choices []Choice `json:"choices,omitempty"` // select and icon: the values allowed
	Default string   `json:"default,omitempty"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"` // characters, or the largest int
	I18n    bool     `json:"i18n,omitempty"`
	// One (a bool of an item): at most one item of the block has it on,
	// such as the highlighted plan of a pricing table.
	One bool `json:"one,omitempty"`
	// Hints are the editor's placeholders for the translations (lang ->
	// text): what a theme's section shows until it is edited.
	Hints map[string]string `json:"hints,omitempty"`
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Type is a block type. Where lists the regions it may be placed in ("*"
// for every region); Once means at most one per region.
type Type struct {
	Type   string      `json:"type"`
	Label  string      `json:"label"`
	Icon   string      `json:"icon"`
	Help   string      `json:"help,omitempty"`
	Where  []string    `json:"where"`
	Once   bool        `json:"once,omitempty"`
	Fields []Field     `json:"fields"`
	Items  *ItemSchema `json:"items,omitempty"`
}

// ItemSchema is the list a block repeats: the features of a feature grid,
// the questions of a FAQ. Each item has its own settings and translations,
// checked against Fields like a block's.
type ItemSchema struct {
	Label  string  `json:"label"` // one item, such as "Feature"
	Add    string  `json:"add"`   // the editor's button, such as "Add a feature"
	Max    int     `json:"max"`
	Fields []Field `json:"fields"`
}

// Field returns the item field named key.
func (s *ItemSchema) Field(key string) (*Field, bool) {
	for i := range s.Fields {
		if s.Fields[i].Key == key {
			return &s.Fields[i], true
		}
	}
	return nil, false
}

// Regions: the global header and footer, and one main area per page type.
// "front" is the front page as a landing (Settings → Reading), which a
// layout can build in any theme.
var (
	PageKinds = []string{"front", "home", "single", "page", "archive", "search", "404"}
	Regions   = append([]string{"header", "footer"}, PageKinds...)
)

// Section anchors (#features): an id a menu link or a button can point to.
var reservedAnchors = map[string]bool{"main": true, "menu": true, "primary-menu": true, "comments": true, "respond": true, "search": true}

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

// StyleOrder is the order section style fields are shown in. animation
// and sequence are the section's motion (internal/motion): the whole
// section plays the animation, or with sequence "on" its blocks do, one
// after another.
var StyleOrder = []string{"background", "padding", "width", "align", "animation", "sequence"}

func init() {
	SectionStyles["animation"] = motion.Names()
	SectionStyles["sequence"] = []string{"off", "on"}
	// Every block can be animated.
	for i := range Types {
		Types[i].Fields = append(Types[i].Fields, AnimationField())
	}
}

// AnimationField is the animation every block has, the theme's sections
// too.
func AnimationField() Field {
	return Field{Key: "animation", Type: "select", Label: "Animation", Default: motion.None, Choices: animationChoices()}
}

// animationChoices are the motion library's animations, after None.
func animationChoices() []Choice {
	out := []Choice{{Value: motion.None, Label: "None"}}
	for _, a := range motion.Animations {
		out = append(out, Choice{Value: a.Name, Label: a.Label})
	}
	return out
}

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
	listing   = []string{"front", "home", "archive", "search"}
	content   = []string{"single", "page"}

	// The heading and introduction of the landing blocks.
	heading = Field{Key: "title", Type: "text", Label: "Heading", Max: 160, I18n: true}
	intro   = Field{Key: "intro", Type: "textarea", Label: "Introduction", Max: 500, I18n: true}
)

// Icons are the Lucide icons a block can show (a feature's icon): a curated
// set, each compiled into the binary (scripts/icons.txt).
var Icons = choices(
	"sparkles", "Sparkles", "zap", "Lightning", "rocket", "Rocket", "shield-check", "Shield", "lock", "Lock",
	"gauge", "Gauge", "timer", "Timer", "chart-line", "Line chart", "trending-up", "Growth", "target", "Target",
	"layers", "Layers", "puzzle", "Puzzle piece", "plug", "Plug", "code", "Code", "terminal", "Terminal",
	"server", "Server", "database", "Database", "cloud", "Cloud", "globe", "Globe", "languages", "Languages",
	"smartphone", "Phone", "palette", "Palette", "wand-sparkles", "Magic wand", "lightbulb", "Light bulb", "heart", "Heart",
	"handshake", "Handshake", "users", "People", "message-circle", "Chat", "headphones", "Headphones", "mail", "E-mail",
	"calendar", "Calendar", "map-pin", "Map pin", "truck", "Delivery", "shopping-bag", "Shopping bag", "credit-card", "Card payment",
	"gift", "Gift", "award", "Award", "star", "Star", "leaf", "Leaf", "graduation-cap", "Education",
	"briefcase", "Briefcase", "coffee", "Coffee",
)

// MaxHTML is the size of an HTML block, in characters, per language.
const MaxHTML = 50000

// classHelp explains the CSS class fields of sections and HTML blocks.
const classHelp = "Optional. Up to 5 class names separated by spaces (lower-case letters, digits and hyphens, starting with a letter), for your Custom CSS."

// MaxItems bounds the items of any block.
const MaxItems = 30

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
	{Type: "html", Label: "HTML", Icon: "code", Where: everwhere,
		Help: "Your own markup: sections, text, links, images, lists, tables. Scripts, <style> and style=\"\" are removed; the looks go in Custom CSS.",
		Fields: []Field{
			{Key: "html", Type: "html", Label: "HTML", Max: MaxHTML, I18n: true,
				Help: "Allowed: structural and text elements, links, images, lists, tables, figure, details, and class, id, aria-* and data-* attributes. Scripts, <style>, style=\"\" attributes, event handlers (onclick…), iframes and forms are removed when saved: the site's security policy would block them. Give elements a class and style them in Custom CSS."},
			{Key: "class", Type: "class", Label: "CSS class", Help: classHelp},
			onOff("prose", "Use the theme's text styles", "true"),
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
	// Landing blocks: the sections of a landing page or a one-page site.
	{Type: "features", Label: "Features", Icon: "grid-2x2", Where: everwhere,
		Help: "A grid of features, each with an icon, a title and a short text.",
		Fields: []Field{heading, intro,
			{Key: "columns", Type: "select", Label: "Columns", Default: "3", Choices: choices("2", "Two", "3", "Three", "4", "Four")},
			{Key: "style", Type: "select", Label: "Style", Default: "cards", Choices: choices("cards", "Cards", "plain", "Plain")},
			{Key: "align", Type: "select", Label: "Alignment", Default: "left", Choices: alignLC},
		},
		Items: &ItemSchema{Label: "Feature", Add: "Add a feature", Max: 12, Fields: []Field{
			{Key: "icon", Type: "icon", Label: "Icon", Default: "sparkles", Choices: Icons},
			{Key: "title", Type: "text", Label: "Title", Max: 100, I18n: true},
			{Key: "text", Type: "textarea", Label: "Text", Max: 400, I18n: true},
		}}},
	{Type: "stats", Label: "Stats", Icon: "chart-column", Where: everwhere,
		Help:   "Key figures, each a large value with a label.",
		Fields: []Field{heading, intro, {Key: "align", Type: "select", Label: "Alignment", Default: "center", Choices: alignLC}},
		Items: &ItemSchema{Label: "Figure", Add: "Add a figure", Max: 8, Fields: []Field{
			{Key: "value", Type: "text", Label: "Value", Max: 24, I18n: true, Help: "Such as 10k+, 4.9/5 or 24/7."},
			{Key: "label", Type: "text", Label: "Label", Max: 100, I18n: true},
		}}},
	{Type: "testimonials", Label: "Testimonials", Icon: "quote", Where: everwhere,
		Help: "Quotes from customers or readers, with their name, role and photo.",
		Fields: []Field{heading, intro,
			{Key: "columns", Type: "select", Label: "Columns", Default: "3", Choices: choices("1", "One", "2", "Two", "3", "Three")},
		},
		Items: &ItemSchema{Label: "Testimonial", Add: "Add a testimonial", Max: 12, Fields: []Field{
			{Key: "quote", Type: "textarea", Label: "Quote", Max: 600, I18n: true},
			{Key: "name", Type: "text", Label: "Name", Max: 80},
			{Key: "role", Type: "text", Label: "Role", Max: 120, I18n: true, Help: "Such as their job and company."},
			{Key: "image", Type: "image", Label: "Photo"},
		}}},
	{Type: "pricing", Label: "Pricing", Icon: "tag", Where: everwhere,
		Help: "Plans you write yourself: name, price, features and a button. One can be highlighted.",
		Fields: []Field{heading, intro,
			{Key: "badge", Type: "text", Label: "Label of the highlighted plan", Max: 40, I18n: true, Default: "Most popular"},
		},
		Items: &ItemSchema{Label: "Plan", Add: "Add a plan", Max: 6, Fields: []Field{
			{Key: "name", Type: "text", Label: "Name", Max: 60, I18n: true},
			{Key: "price", Type: "text", Label: "Price", Max: 30, I18n: true, Help: "Such as 19 € or Free."},
			{Key: "period", Type: "text", Label: "Period", Max: 40, I18n: true, Help: "Such as per month."},
			{Key: "description", Type: "textarea", Label: "Description", Max: 300, I18n: true},
			{Key: "features", Type: "textarea", Label: "Features", Max: 1500, I18n: true, Help: "One per line."},
			{Key: "button_label", Type: "text", Label: "Button label", Max: 60, I18n: true},
			{Key: "button_url", Type: "url", Label: "Button link"},
			{Key: "highlight", Type: "bool", Label: "Highlight this plan", Default: "false", One: true},
		}}},
	{Type: "faq", Label: "Questions and answers", Icon: "circle-question-mark", Where: everwhere,
		Help:   "Frequently asked questions; each answer opens under its question, without JavaScript.",
		Fields: []Field{heading, intro, onOff("first_open", "Open the first answer", "false")},
		Items: &ItemSchema{Label: "Question", Add: "Add a question", Max: MaxItems, Fields: []Field{
			{Key: "question", Type: "text", Label: "Question", Max: 200, I18n: true},
			{Key: "answer", Type: "markdown", Label: "Answer", Max: 3000, I18n: true, Help: "Markdown: **bold**, _italic_, [links](https://…) and lists."},
		}}},
	{Type: "logos", Label: "Logos", Icon: "images", Where: everwhere,
		Help: "A row of client or partner logos from the media library.",
		Fields: []Field{heading,
			{Key: "size", Type: "select", Label: "Size", Default: "medium", Choices: sizesSML},
			onOff("muted", "Grey until pointed at", "true"),
		},
		Items: &ItemSchema{Label: "Logo", Add: "Add a logo", Max: 24, Fields: []Field{
			{Key: "image", Type: "image", Label: "Logo"},
			{Key: "alt", Type: "text", Label: "Name", Max: 120, I18n: true, Help: "Whose logo it is, for people who cannot see it. Empty uses the media library's text."},
			{Key: "link", Type: "url", Label: "Link"},
		}}},
	{Type: "cta", Label: "Call to action", Icon: "megaphone", Where: everwhere,
		Help: "A heading, a short text and one or two buttons.",
		Fields: []Field{
			{Key: "title", Type: "text", Label: "Heading", Max: 160, I18n: true, Default: "Ready to start?"},
			{Key: "text", Type: "textarea", Label: "Text", Max: 500, I18n: true},
			{Key: "primary_label", Type: "text", Label: "Main button label", Max: 60, I18n: true, Default: "Get started"},
			{Key: "primary_url", Type: "url", Label: "Main button link", Default: "/"},
			{Key: "secondary_label", Type: "text", Label: "Second button label", Max: 60, I18n: true},
			{Key: "secondary_url", Type: "url", Label: "Second button link"},
			{Key: "style", Type: "select", Label: "Style", Default: "panel", Choices: choices("panel", "Panel", "plain", "Plain")},
			{Key: "align", Type: "select", Label: "Alignment", Default: "center", Choices: alignLC},
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

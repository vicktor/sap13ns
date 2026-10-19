package builder

import "strconv"

// Ready-made content for the editor: the sample items a new landing block
// starts with, and the front page presets that fill the "front" region with
// a whole landing page. The texts are English UI strings: the caller passes
// a translator into the site's main language, and Localize adds the
// translations into the other languages. They are placeholders the owner
// replaces, so they stay neutral.

// PresetNames are the front page presets, in the order the editor offers
// them.
var PresetNames = []string{"landing", "one-page"}

// item builds an item from key, value pairs; empty values are left out.
func item(kv ...string) Item {
	s := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			s[kv[i]] = kv[i+1]
		}
	}
	return Item{Settings: s}
}

// block builds a block from key, value pairs (the id is set later).
func block(typ string, items []Item, kv ...string) Block {
	b := Block{Type: typ, Items: items}
	if it := item(kv...); len(it.Settings) > 0 {
		b.Settings = it.Settings
	}
	return b
}

// Starters returns the block that a new block of each type with items starts
// as in the editor: a few sample items to edit rather than an empty list.
func Starters(t func(string, ...any) string) map[string]Block {
	return map[string]Block{
		"features":     block("features", sampleFeatures(t)),
		"stats":        block("stats", sampleStats(t)),
		"testimonials": block("testimonials", sampleTestimonials(t)),
		"pricing":      block("pricing", samplePlans(t, "/")),
		"faq":          block("faq", sampleQuestions(t)),
		"logos":        block("logos", nil),
	}
}

func sampleFeatures(t func(string, ...any) string) []Item {
	return []Item{
		item("icon", "sparkles", "title", t("Simple to use"),
			"text", t("Say in a sentence or two why this matters to the people you write for.")),
		item("icon", "zap", "title", t("Fast results"),
			"text", t("Replace this text with a benefit your visitors care about.")),
		item("icon", "shield-check", "title", t("Reliable"),
			"text", t("Each feature has an icon, a title and a short text. Add as many as you need.")),
	}
}

func sampleStats(t func(string, ...any) string) []Item {
	return []Item{
		item("value", "10k+", "label", t("Happy customers")),
		item("value", "4.9/5", "label", t("Average rating")),
		item("value", "24/7", "label", t("Support")),
		item("value", "12", "label", t("Years of experience")), // four fill a half-width column's 2x2 grid
	}
}

func sampleTestimonials(t func(string, ...any) string) []Item {
	return []Item{
		item("quote", t("Working with them was easy from the first day. The results speak for themselves."),
			"name", "Alex Morgan", "role", t("Founder, Example Studio")),
		item("quote", t("Clear, quick and friendly. I would recommend them to anyone."),
			"name", "Sam Rivera", "role", t("Marketing lead")),
		item("quote", t("Exactly what we needed, and it took a fraction of the time we expected."),
			"name", "Jordan Lee", "role", t("Product manager")),
	}
}

// samplePlans are three plans; link is where their buttons go.
func samplePlans(t func(string, ...any) string, link string) []Item {
	return []Item{
		item("name", t("Starter"), "price", t("Free"), "description", t("For trying things out."),
			"features", t("One project")+"\n"+t("Basic statistics")+"\n"+t("Community support"),
			"button_label", t("Get started"), "button_url", link),
		item("name", t("Pro"), "price", "19 €", "period", t("per month"), "description", t("For people who need more."),
			"features", t("Unlimited projects")+"\n"+t("Detailed statistics")+"\n"+t("Priority support"),
			"button_label", t("Choose Pro"), "button_url", link, "highlight", "true"),
		item("name", t("Business"), "price", "49 €", "period", t("per month"), "description", t("For teams and companies."),
			"features", t("Everything in Pro")+"\n"+t("Team accounts")+"\n"+t("A personal contact"),
			"button_label", t("Contact us"), "button_url", link),
	}
}

func sampleQuestions(t func(string, ...any) string) []Item {
	return []Item{
		item("question", t("How do I get started?"),
			"answer", t("Write the answer here. Answers can use Markdown: **bold**, _italic_, links and lists.")),
		item("question", t("Can I change my mind later?"),
			"answer", t("Yes. Say here what people can change, and how.")),
		item("question", t("How can I reach you?"),
			"answer", t("Tell visitors the best way to contact you and how soon they can expect an answer.")),
	}
}

// Preset returns a front page region: "landing" (hero, features,
// testimonials, pricing and a call to action) or "one-page" (every section
// with an anchor, for a menu of #links). Its ids are placeholders that
// Normalize, or the editor, replaces; nil for an unknown name.
func Preset(name string, t func(string, ...any) string) *Region {
	n := 0
	sec := func(anchor, layout string, style map[string]string, cols ...[]Block) Section {
		n++
		s := Section{ID: "s-" + strconv.Itoa(n), Anchor: anchor, Layout: layout, Style: style, Columns: cols}
		for ci := range s.Columns {
			for bi := range s.Columns[ci] {
				n++
				s.Columns[ci][bi].ID = "b-" + strconv.Itoa(n)
			}
		}
		return s
	}
	col := func(bs ...Block) []Block { return bs }
	style := func(kv ...string) map[string]string { return item(kv...).Settings }
	switch name {
	case "landing":
		return &Region{Sections: []Section{
			sec("", "1", style("padding", "large"), col(block("hero", nil,
				"button_label", t("See the plans"), "button_url", "#pricing", "size", "large"))),
			sec("features", "1", style("width", "wide"), col(block("features", sampleFeatures(t),
				"title", t("Why people choose us"), "intro", t("Three good reasons, in a few words each.")))),
			sec("", "1", style("background", "muted"), col(block("testimonials", sampleTestimonials(t),
				"title", t("What our customers say")))),
			sec("pricing", "1", style("width", "wide"), col(block("pricing", samplePlans(t, "#contact"),
				"title", t("Simple pricing"), "intro", t("Pick the plan that suits you. You can change it at any time.")))),
			sec("contact", "1", nil, col(block("cta", nil,
				"title", t("Ready to start?"), "text", t("Tell us what you need and we will get back to you within a day."),
				"primary_label", t("Contact us"), "primary_url", "mailto:hello@example.com"))),
		}}
	case "one-page":
		return &Region{Sections: []Section{
			sec("intro", "1", style("padding", "large"), col(block("hero", nil,
				"button_label", t("Get in touch"), "button_url", "#contact", "size", "large"))),
			sec("about", "1-1", nil,
				col(block("rich-text", nil, "text", "## "+t("About us")+"\n\n"+
					t("Introduce yourself here: who you are, what you do and who you do it for. A short paragraph is enough."))),
				col(block("stats", sampleStats(t)))),
			sec("services", "1", style("background", "muted", "width", "wide"), col(block("features", sampleFeatures(t),
				"title", t("What we do"), "intro", t("The services you offer, one card each.")))),
			sec("testimonials", "1", nil, col(block("testimonials", sampleTestimonials(t),
				"title", t("What our customers say")))),
			sec("faq", "1", style("width", "narrow"), col(block("faq", sampleQuestions(t),
				"title", t("Questions and answers"), "first_open", "true"))),
			sec("contact", "1", nil, col(block("cta", nil,
				"title", t("Let us talk"), "text", t("Tell us what you need and we will get back to you within a day."),
				"primary_label", t("Write to us"), "primary_url", "mailto:hello@example.com",
				"secondary_label", t("Back to top"), "secondary_url", "#intro"))),
		}}
	}
	return nil
}

// Localize adds to dst, built from a preset or a starter in the main
// language, the texts of other, built from the same one in lang, wherever
// they differ: each translatable setting of each block and item. A text
// that reads the same in both languages is left untranslated.
func Localize(dst, other *Region, lang string) {
	if dst == nil || other == nil || len(dst.Sections) != len(other.Sections) {
		return
	}
	for si := range dst.Sections {
		ds, xs := &dst.Sections[si], &other.Sections[si]
		for ci := range ds.Columns {
			if ci >= len(xs.Columns) || len(xs.Columns[ci]) != len(ds.Columns[ci]) {
				continue
			}
			for bi := range ds.Columns[ci] {
				LocalizeBlock(&ds.Columns[ci][bi], &xs.Columns[ci][bi], lang)
			}
		}
	}
}

// LocalizeBlock is Localize for one block and its items.
func LocalizeBlock(dst, other *Block, lang string) {
	t, ok := TypeOf(dst.Type)
	if !ok || dst.Type != other.Type {
		return
	}
	dst.I18n = localized(dst.Settings, other.Settings, dst.I18n, t.Field, lang)
	if t.Items == nil || len(dst.Items) != len(other.Items) {
		return
	}
	for i := range dst.Items {
		dst.Items[i].I18n = localized(dst.Items[i].Settings, other.Items[i].Settings, dst.Items[i].I18n, t.Items.Field, lang)
	}
}

func localized(main, other map[string]string, tr map[string]map[string]string, field func(string) (*Field, bool), lang string) map[string]map[string]string {
	for k, v := range other {
		f, ok := field(k)
		if !ok || !f.I18n || v == "" || v == main[k] {
			continue
		}
		if tr == nil {
			tr = map[string]map[string]string{}
		}
		if tr[lang] == nil {
			tr[lang] = map[string]string{}
		}
		tr[lang][k] = v
	}
	return tr
}

// Package feed renders RSS 2.0, Atom 1.0 and JSON Feed 1.1 documents.
package feed

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"time"
)

type Item struct {
	ID          string // stable permalink-based id
	Title       string
	URL         string
	Summary     string
	ContentHTML string
	AuthorName  string
	AuthorURL   string
	Published   time.Time
	Updated     time.Time
	Categories  []string
	ImageURL    string
}

type Feed struct {
	Title       string
	Description string
	SiteURL     string
	FeedURL     string // URL of this document
	Language    string
	Updated     time.Time
	HubURL      string // WebSub hub, optional
	IconURL     string
	Items       []Item
}

// RSS 2.0 ----------------------------------------------------------------

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Content string     `xml:"xmlns:content,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	DC      string     `xml:"xmlns:dc,attr"`
	Channel rssChannel `xml:"channel"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type rssChannel struct {
	Title         string     `xml:"title"`
	Link          string     `xml:"link"`
	Description   string     `xml:"description"`
	Language      string     `xml:"language,omitempty"`
	LastBuildDate string     `xml:"lastBuildDate,omitempty"`
	Generator     string     `xml:"generator"`
	AtomLinks     []atomLink `xml:"atom:link"`
	Items         []rssItem  `xml:"item"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

// cdata is emitted as a CDATA section; encoding/xml splits any "]]>" safely.
type cdata struct {
	Value string `xml:",cdata"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Creator     string   `xml:"dc:creator,omitempty"`
	Categories  []string `xml:"category"`
	Description cdata    `xml:"description"`
	Content     *cdata   `xml:"content:encoded,omitempty"`
}

func (f *Feed) WriteRSS(w io.Writer) error {
	ch := rssChannel{
		Title:       f.Title,
		Link:        f.SiteURL,
		Description: f.Description,
		Language:    f.Language,
		Generator:   "sap13ns",
		AtomLinks:   []atomLink{{Href: f.FeedURL, Rel: "self", Type: "application/rss+xml"}},
	}
	if f.HubURL != "" {
		ch.AtomLinks = append(ch.AtomLinks, atomLink{Href: f.HubURL, Rel: "hub"})
	}
	if !f.Updated.IsZero() {
		ch.LastBuildDate = f.Updated.UTC().Format(time.RFC1123Z)
	}
	for _, it := range f.Items {
		ri := rssItem{
			Title:       it.Title,
			Link:        it.URL,
			GUID:        rssGUID{Value: it.ID, IsPermaLink: it.ID == it.URL},
			PubDate:     it.Published.UTC().Format(time.RFC1123Z),
			Creator:     it.AuthorName,
			Categories:  it.Categories,
			Description: cdata{it.Summary},
		}
		if it.ContentHTML != "" {
			ri.Content = &cdata{it.ContentHTML}
		}
		ch.Items = append(ch.Items, ri)
	}
	doc := rssDoc{Version: "2.0", Content: "http://purl.org/rss/1.0/modules/content/", Atom: "http://www.w3.org/2005/Atom",
		DC: "http://purl.org/dc/elements/1.1/", Channel: ch}
	return writeXML(w, doc)
}

// Atom 1.0 ---------------------------------------------------------------

type atomFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	NS        string      `xml:"xmlns,attr"`
	Lang      string      `xml:"xml:lang,attr,omitempty"`
	ID        string      `xml:"id"`
	Title     string      `xml:"title"`
	Subtitle  string      `xml:"subtitle,omitempty"`
	Updated   string      `xml:"updated"`
	Links     []atomLink  `xml:"link"`
	Generator string      `xml:"generator"`
	Icon      string      `xml:"icon,omitempty"`
	Entries   []atomEntry `xml:"entry"`
}

type atomText struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type atomPerson struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

type atomEntry struct {
	ID         string         `xml:"id"`
	Title      atomText       `xml:"title"`
	Links      []atomLink     `xml:"link"`
	Published  string         `xml:"published"`
	Updated    string         `xml:"updated"`
	Author     *atomPerson    `xml:"author,omitempty"`
	Categories []atomCategory `xml:"category"`
	Summary    atomText       `xml:"summary"`
	Content    *atomText      `xml:"content,omitempty"`
}

func (f *Feed) WriteAtom(w io.Writer) error {
	af := atomFeed{
		NS:        "http://www.w3.org/2005/Atom",
		Lang:      f.Language,
		ID:        f.FeedURL,
		Title:     f.Title,
		Subtitle:  f.Description,
		Updated:   f.Updated.UTC().Format(time.RFC3339),
		Generator: "sap13ns",
		Icon:      f.IconURL,
		Links: []atomLink{
			{Href: f.FeedURL, Rel: "self", Type: "application/atom+xml"},
			{Href: f.SiteURL, Rel: "alternate", Type: "text/html"},
		},
	}
	if f.HubURL != "" {
		af.Links = append(af.Links, atomLink{Href: f.HubURL, Rel: "hub"})
	}
	for _, it := range f.Items {
		e := atomEntry{
			ID:        it.ID,
			Title:     atomText{Type: "text", Value: it.Title},
			Links:     []atomLink{{Href: it.URL, Rel: "alternate", Type: "text/html"}},
			Published: it.Published.UTC().Format(time.RFC3339),
			Updated:   it.Updated.UTC().Format(time.RFC3339),
			Summary:   atomText{Type: "text", Value: it.Summary},
		}
		if it.AuthorName != "" {
			e.Author = &atomPerson{Name: it.AuthorName, URI: it.AuthorURL}
		}
		for _, c := range it.Categories {
			e.Categories = append(e.Categories, atomCategory{Term: c})
		}
		if it.ContentHTML != "" {
			e.Content = &atomText{Type: "html", Value: it.ContentHTML}
		}
		af.Entries = append(af.Entries, e)
	}
	return writeXML(w, af)
}

// JSON Feed 1.1 ----------------------------------------------------------

type jsonFeed struct {
	Version     string     `json:"version"`
	Title       string     `json:"title"`
	HomePageURL string     `json:"home_page_url"`
	FeedURL     string     `json:"feed_url"`
	Description string     `json:"description,omitempty"`
	Language    string     `json:"language,omitempty"`
	Icon        string     `json:"icon,omitempty"`
	Hubs        []jsonHub  `json:"hubs,omitempty"`
	Items       []jsonItem `json:"items"`
}

type jsonHub struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type jsonAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type jsonItem struct {
	ID            string       `json:"id"`
	URL           string       `json:"url"`
	Title         string       `json:"title"`
	ContentHTML   string       `json:"content_html,omitempty"`
	ContentText   string       `json:"content_text,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Image         string       `json:"image,omitempty"`
	DatePublished string       `json:"date_published"`
	DateModified  string       `json:"date_modified"`
	Authors       []jsonAuthor `json:"authors,omitempty"`
	Tags          []string     `json:"tags,omitempty"`
}

func (f *Feed) WriteJSON(w io.Writer) error {
	jf := jsonFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       f.Title,
		HomePageURL: f.SiteURL,
		FeedURL:     f.FeedURL,
		Description: f.Description,
		Language:    f.Language,
		Icon:        f.IconURL,
		Items:       []jsonItem{},
	}
	if f.HubURL != "" {
		jf.Hubs = []jsonHub{{Type: "WebSub", URL: f.HubURL}}
	}
	for _, it := range f.Items {
		ji := jsonItem{
			ID:            it.ID,
			URL:           it.URL,
			Title:         it.Title,
			ContentHTML:   it.ContentHTML,
			Summary:       it.Summary,
			Image:         it.ImageURL,
			DatePublished: it.Published.UTC().Format(time.RFC3339),
			DateModified:  it.Updated.UTC().Format(time.RFC3339),
			Tags:          it.Categories,
		}
		if ji.ContentHTML == "" {
			ji.ContentText = it.Summary
		}
		if it.AuthorName != "" {
			ji.Authors = []jsonAuthor{{Name: it.AuthorName, URL: it.AuthorURL}}
		}
		jf.Items = append(jf.Items, ji)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	return enc.Encode(jf)
}

func writeXML(w io.Writer, v any) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return enc.Flush()
}

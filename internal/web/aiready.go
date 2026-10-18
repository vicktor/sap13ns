package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/content"
	"sap13ns/internal/store"
)

// AI-ready publishing: /llms.txt (an index for language models, see
// llmstxt.org), /llms-full.txt (every public text in Markdown), a .md twin
// of each post and page, and robots.txt rules for AI crawlers. All of it is
// governed by Settings.AIAccess.

// Crawlers that collect training data, and agents that fetch pages to answer
// a user's question or to build an AI search index.
var (
	aiTrainingBots = []string{"GPTBot", "ClaudeBot", "anthropic-ai", "Google-Extended", "Applebot-Extended", "CCBot",
		"Bytespider", "Meta-ExternalAgent", "cohere-training-data-crawler", "Diffbot", "AI2Bot", "Amazonbot",
		"PanguBot", "Timpibot", "omgili", "ImagesiftBot"}
	aiAnswerBots = []string{"OAI-SearchBot", "ChatGPT-User", "Claude-SearchBot", "Claude-User", "PerplexityBot",
		"Perplexity-User", "DuckAssistBot", "MistralAI-User", "YouBot", "Meta-ExternalFetcher", "Google-CloudVertexBot"}
)

const llmsFullMax = 8 << 20 // bytes; keeps /llms-full.txt reasonable on big sites

func (s *Server) aiOpen() bool { return s.settings().AIAccess != "block" }

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	st := s.settings()
	if st.DiscourageSearch {
		fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
		return
	}
	var b strings.Builder
	block := func(bots []string) {
		for _, bot := range bots {
			fmt.Fprintf(&b, "User-agent: %s\n", bot)
		}
		b.WriteString("Disallow: /\n\n")
	}
	switch st.AIAccess {
	case "block":
		block(append(append([]string{}, aiTrainingBots...), aiAnswerBots...))
	case "no-training":
		block(aiTrainingBots)
	}
	b.WriteString("User-agent: *\nDisallow: /admin/\nDisallow: /login\nDisallow: /search\nAllow: /\n")
	switch st.AIAccess {
	case "no-training":
		b.WriteString("Content-Signal: search=yes, ai-input=yes, ai-train=no\n")
	case "block":
		b.WriteString("Content-Signal: search=yes, ai-input=no, ai-train=no\n")
	}
	fmt.Fprintf(&b, "\nSitemap: %s\n", s.cfg.AbsURL("/sitemap.xml"))
	w.Write([]byte(b.String()))
}

// markdownURL is the address of a post's Markdown twin.
func markdownURL(postURL string) string {
	if strings.HasSuffix(postURL, "/") {
		return postURL + "index.md"
	}
	return postURL + ".md"
}

// serveMarkdown answers /blog/{slug}.md and /{slug}.md.
func (s *Server) serveMarkdown(w http.ResponseWriter, r *http.Request, typ, slug string) {
	if !s.aiOpen() {
		s.notFound(w, r)
		return
	}
	p, err := s.store.PostBySlug(r.Context(), typ, s.reqLang(r), slug)
	if err != nil || !p.IsPublic(time.Now()) {
		s.notFound(w, r)
		return
	}
	var b bytes.Buffer
	s.writeMarkdown(&b, p, true)
	h := w.Header()
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Link", `<`+s.cfg.AbsURL(s.postURL(p))+`>; rel="canonical"`)
	w.Write(b.Bytes())
}

// writeMarkdown renders a post as Markdown, optionally with front matter.
func (s *Server) writeMarkdown(b *bytes.Buffer, p *store.Post, front bool) {
	abs := s.cfg.AbsURL(s.postURL(p))
	if front {
		b.WriteString("---\n")
		fmt.Fprintf(b, "title: %s\n", strconv.Quote(p.Title))
		fmt.Fprintf(b, "url: %s\n", abs)
		fmt.Fprintf(b, "lang: %s\n", p.Lang)
		if p.PublishedAt != nil {
			fmt.Fprintf(b, "date: %s\n", p.PublishedAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(b, "updated: %s\n", p.UpdatedAt.UTC().Format(time.RFC3339))
		if p.Type == store.TypePost && p.AuthorName != "" {
			fmt.Fprintf(b, "author: %s\n", strconv.Quote(p.AuthorName))
		}
		if cats := p.Categories(); len(cats) > 0 {
			names := make([]string, len(cats))
			for i, c := range cats {
				names[i] = strconv.Quote(c.In(p.Lang).Name)
			}
			fmt.Fprintf(b, "categories: [%s]\n", strings.Join(names, ", "))
		}
		if d := content.Summary(p.Excerpt, p.Content, 300); d != "" {
			fmt.Fprintf(b, "description: %s\n", strconv.Quote(d))
		}
		b.WriteString("---\n\n")
	}
	fmt.Fprintf(b, "# %s\n\n", p.Title)
	if !front {
		fmt.Fprintf(b, "URL: %s\n", abs)
		if p.PublishedAt != nil {
			fmt.Fprintf(b, "Published: %s\n", p.PublishedAt.UTC().Format("2006-01-02"))
		}
		b.WriteString("\n")
	}
	b.WriteString(strings.TrimSpace(p.Content))
	b.WriteString("\n")
}

func (s *Server) publicPosts(r *http.Request, typ string, limit int) ([]store.Post, error) {
	order := ""
	if typ == store.TypePage {
		order = "menu"
	}
	list, _, err := s.store.ListPosts(r.Context(), store.PostQuery{Type: typ, Lang: s.reqLang(r), PublicOnly: true,
		Order: order, Limit: limit, WithTerms: true})
	return list, err
}

// handleLLMs serves the llms.txt index for the request's language.
func (s *Server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	if !s.aiOpen() {
		s.notFound(w, r)
		return
	}
	lang := s.reqLang(r)
	txt := s.settings().Text(lang)
	pages, err := s.publicPosts(r, store.TypePage, 200)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	posts, err := s.publicPosts(r, store.TypePost, 300)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", txt.SiteTitle)
	if txt.Tagline != "" {
		fmt.Fprintf(&b, "> %s\n\n", oneLine(txt.Tagline))
	}
	b.WriteString(s.i18n.T(lang, "Every post and page is also available as Markdown: add .md to its address.") + "\n\n")
	item := func(p *store.Post) {
		fmt.Fprintf(&b, "- [%s](%s)", mdText(p.Title), s.cfg.AbsURL(markdownURL(s.postURL(p))))
		if d := content.Summary(p.Excerpt, p.Content, 200); d != "" {
			fmt.Fprintf(&b, ": %s", oneLine(d))
		}
		b.WriteString("\n")
	}
	if len(pages) > 0 {
		b.WriteString("## " + s.i18n.T(lang, "Pages") + "\n\n")
		for i := range pages {
			item(&pages[i])
		}
		b.WriteString("\n")
	}
	if len(posts) > 0 {
		b.WriteString("## " + s.i18n.T(lang, "Posts") + "\n\n")
		for i := range posts {
			item(&posts[i])
		}
		b.WriteString("\n")
	}
	b.WriteString("## Optional\n\n")
	fmt.Fprintf(&b, "- [%s](%s)\n", s.i18n.T(lang, "Full text of the site"), s.cfg.AbsURL(s.langPath(lang, "/llms-full.txt")))
	fmt.Fprintf(&b, "- [RSS](%s)\n", s.cfg.AbsURL(s.langPath(lang, "/feed.xml")))
	for _, l := range s.siteLangs() {
		if l != lang {
			fmt.Fprintf(&b, "- [%s](%s)\n", langName(l), s.cfg.AbsURL(s.langPath(l, "/llms.txt")))
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b.Bytes())
}

// handleLLMsFull concatenates every public page and post as Markdown.
func (s *Server) handleLLMsFull(w http.ResponseWriter, r *http.Request) {
	if !s.aiOpen() {
		s.notFound(w, r)
		return
	}
	lang := s.reqLang(r)
	txt := s.settings().Text(lang)
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", txt.SiteTitle)
	if txt.Tagline != "" {
		fmt.Fprintf(&b, "> %s\n\n", oneLine(txt.Tagline))
	}
	for _, typ := range []string{store.TypePage, store.TypePost} {
		list, err := s.publicPosts(r, typ, 2000)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for i := range list {
			if b.Len() > llmsFullMax {
				break
			}
			b.WriteString("\n---\n\n")
			s.writeMarkdown(&b, &list[i], false)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b.Bytes())
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// mdText escapes the characters that would end a Markdown link label.
func mdText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`).Replace(oneLine(s))
}

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The texts visitors see, in English and Spanish (the languages of the
// site's interface); any other language gets English. The script asset has
// no text of its own: it reads its labels from the page.
type texts struct {
	es bool
}

func textsFor(lang string) texts {
	lang = strings.ToLower(strings.TrimSpace(lang))
	return texts{es: lang == "es" || strings.HasPrefix(lang, "es-") || strings.HasPrefix(lang, "es_")}
}

func (t texts) pick(en, es string) string {
	if t.es {
		return es
	}
	return en
}

// Label names a gallery for assistive technology.
func (t texts) Label(src Source, name string) string {
	switch {
	case name == "":
		return t.pick("Latest videos on YouTube", "Últimos vídeos en YouTube")
	case src.Kind == kindPlaylist:
		return t.pick("Videos in the playlist "+name+" on YouTube", "Vídeos de la lista "+name+" en YouTube")
	}
	return t.pick("Latest videos from "+name+" on YouTube", "Últimos vídeos de "+name+" en YouTube")
}

func (t texts) Play(title string) string {
	return t.pick("Play video: "+title+" (YouTube)", "Reproducir vídeo: "+title+" (YouTube)")
}

func (t texts) More(src Source) string {
	if src.Kind == kindPlaylist {
		return t.pick("Full playlist on YouTube", "Lista completa en YouTube")
	}
	return t.pick("More videos on YouTube", "Más vídeos en YouTube")
}

// Empty is the link shown before the first fetch, or when the list is too
// old to show.
func (t texts) Empty(src Source, name string) string {
	switch {
	case name == "" && src.Kind == kindPlaylist:
		return t.pick("Watch the playlist on YouTube", "Ver la lista en YouTube")
	case name == "":
		return t.pick("Watch the videos on YouTube", "Ver los vídeos en YouTube")
	}
	return t.pick("Watch the videos of "+name+" on YouTube", "Ver los vídeos de "+name+" en YouTube")
}

func (t texts) Prev() string { return t.pick("Previous videos", "Vídeos anteriores") }
func (t texts) Next() string { return t.pick("Next videos", "Vídeos siguientes") }

func (t texts) Live(state string) string {
	if state == "upcoming" {
		return t.pick("Upcoming", "Próximamente")
	}
	return t.pick("Live", "En directo")
}

func (t texts) Policy() string { return t.pick("Privacy policy", "Política de privacidad") }

// Notice is the built-in privacy note. With thumbnails the page loads
// images from YouTube, so the note says so.
func (t texts) Notice(thumbnails bool) string {
	if thumbnails {
		return t.pick("Thumbnails are loaded from YouTube. The player loads from youtube-nocookie.com only when you press play; playing a video connects to YouTube, which may collect data about your visit.",
			"Las miniaturas se cargan desde YouTube. El reproductor se carga desde youtube-nocookie.com solo cuando pulsas reproducir; al reproducir un vídeo te conectas a YouTube, que puede recoger datos de tu visita.")
	}
	return t.pick("Nothing is loaded from YouTube until you press play. Playing a video connects to YouTube, which may collect data about your visit.",
		"No se carga nada de YouTube hasta que pulsas reproducir. Al reproducir un vídeo te conectas a YouTube, que puede recoger datos de tu visita.")
}

var spanishMonths = [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

// Date formats a day like the host's widgets: "Jan 2, 2026" or
// "2 ene 2026".
func (t texts) Date(d time.Time) string {
	if t.es {
		return fmt.Sprintf("%d %s %d", d.Day(), spanishMonths[d.Month()-1], d.Year())
	}
	return fmt.Sprintf("%s %d, %d", d.Month().String()[:3], d.Day(), d.Year())
}

// Clock writes a duration the way video sites show it: 4:13, 1:02:03.
func Clock(sec int) string {
	h, m, s := sec/3600, sec/60%60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// Spoken writes a duration for screen readers, which read "4:13" as a time
// of day: "4 min 13 s", "1 h 2 min".
func Spoken(sec int) string {
	h, m, s := sec/3600, sec/60%60, sec%60
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+" h")
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+" min")
	}
	if s > 0 && h == 0 {
		parts = append(parts, strconv.Itoa(s)+" s")
	}
	return strings.Join(parts, " ")
}

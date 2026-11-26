package main

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// A Google Calendar appointment schedule (a booking page): its address,
// with an optional /u/N/ account path, as Google shows it in "Share" or in
// the src of the "Website embed" code.
var (
	scheduleRe = regexp.MustCompile(`^/calendar(?:/u/\d+)?/appointments/schedules/([A-Za-z0-9_-]{10,200})/?$`)
	iframeRe   = regexp.MustCompile(`(?i)<iframe\b[^>]*?\ssrc\s*=\s*["']([^"']+)["']`)
)

// Booking returns the appointment calendar set in booking_url: the page
// that opens it at Google and the address of its embed. booking_url may be
// the schedule's address or the whole embed code Google gives. ok is false
// when it is set but is not a Google Calendar appointment schedule; the
// calendar is then left out.
func (c Config) Booking() (page, embed string, ok bool) {
	raw := c.get("booking_url")
	if raw == "" {
		return "", "", true
	}
	if m := iframeRe.FindStringSubmatch(raw); m != nil {
		raw = html.UnescapeString(m[1])
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "calendar.google.com" || u.User != nil {
		return "", "", false
	}
	m := scheduleRe.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	page = "https://calendar.google.com/calendar/appointments/schedules/" + m[1]
	return page, page + "?gv=true", true
}

// bookingHTML is the calendar beside the form: a title, a line, a link to
// open it at Google (all there is without JavaScript) and the embed address,
// which contact.js turns into a button that loads the calendar. Nothing is
// requested from Google before the visitor presses it.
func bookingHTML(c Config, t *text, lang, page, embed string) string {
	title := t.bookingTitle
	intro := c.local("booking_text", lang)
	if intro == "" {
		intro = t.bookingText
	}
	var b strings.Builder
	b.WriteString(`<div class="contact-booking">`)
	b.WriteString(`<h2 class="contact-booking-title">` + html.EscapeString(title) + `</h2>`)
	b.WriteString(`<p class="contact-booking-text">` + html.EscapeString(intro) + `</p>`)
	b.WriteString(`<a class="contact-booking-embed" href="` + html.EscapeString(embed) + `" aria-label="` + html.EscapeString(t.bookingShow) + `"></a>`)
	b.WriteString(`<p class="contact-booking-open"><a href="` + html.EscapeString(page) + `" rel="noopener">` + t.bookingOpen + `</a></p>`)
	b.WriteString(`<p class="contact-booking-notice"><small>` + t.bookingNotice + `</small></p>`)
	b.WriteString(`</div>`)
	return b.String()
}

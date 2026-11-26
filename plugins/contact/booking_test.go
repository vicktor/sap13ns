package main

import (
	"strings"
	"testing"
)

func TestBooking(t *testing.T) {
	const page = "https://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234"
	for _, tc := range []struct {
		in   string
		page string
		ok   bool
	}{
		{"", "", true},
		{page, page, true},
		{page + "?gv=true", page, true},
		{"https://calendar.google.com/calendar/u/0/appointments/schedules/AcZssZ1abc-DEF_ghi234/", page, true},
		{`<!-- Google Calendar Appointment Scheduling begin -->
<iframe src="https://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234?gv=true" style="border: 0" width="100%" height="600" frameborder="0"></iframe>`, page, true},
		{"http://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234", "", false},
		{"https://calendar.google.com.evil.test/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234", "", false},
		{"https://calendar.app.google/abcdef", "", false},
		{"https://calendar.google.com/calendar/embed?src=x", "", false},
		{"javascript:alert(1)", "", false},
	} {
		got, embed, ok := Config{Settings: map[string]string{"booking_url": tc.in}}.Booking()
		if got != tc.page || ok != tc.ok || (got != "" && embed != got+"?gv=true") {
			t.Errorf("Booking(%q) = %q, %q, %v; want %q, %v", tc.in, got, embed, ok, tc.page, tc.ok)
		}
	}
}

func TestFormHTMLWithBooking(t *testing.T) {
	cfg := Config{Settings: map[string]string{
		"booking_url":     "https://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234",
		"booking_text_ca": "Tria <b>hora</b>.",
	}}
	got, warn := FormHTML(cfg, "ca")
	for _, want := range []string{
		`<div class="contact-form contact-has-booking"><div class="contact-main"><div data-form="contact"></div></div><div class="contact-booking">`,
		`<h2 class="contact-booking-title">Reserva una cita</h2><p class="contact-booking-text">Tria &lt;b&gt;hora&lt;/b&gt;.</p>`,
		`<a class="contact-booking-embed" href="https://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234?gv=true" aria-label="Mostra el calendari"></a>`,
		`>Obre el calendari a Google</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if warn != "" {
		t.Errorf("warn = %q", warn)
	}
	got, warn = FormHTML(Config{Settings: map[string]string{"booking_url": "https://example.test/book"}}, "es")
	if strings.Contains(got, "contact-booking") || warn == "" {
		t.Errorf("a wrong calendar address: %s (warn %q)", got, warn)
	}
	got, _ = FormHTML(Config{Settings: map[string]string{"booking_url": "https://calendar.google.com/calendar/appointments/schedules/AcZssZ1abc-DEF_ghi234"}}, "es")
	if !strings.Contains(got, ">Ver el calendario<") && !strings.Contains(got, `aria-label="Ver el calendario"`) {
		t.Errorf("Spanish texts: %s", got)
	}
}

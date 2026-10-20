// Package motion lists the animations of the motion library
// (ui/static/motion.css and motion.js): what the page builder and the
// customiser offer, and what they accept. Themes use the same names in
// data-sap-animate="…" and data-sap-stagger="…" (docs/THEMES.md).
package motion

// An Animation is one of the library's: an entrance plays once, when the
// element comes into view; a loop keeps playing from then on.
type Animation struct {
	Name  string
	Label string // English; translated by the admin
	Loop  bool
}

// Animations in the order they are offered.
var Animations = []Animation{
	{Name: "fade", Label: "Fade in"},
	{Name: "fade-up", Label: "Fade in from below"},
	{Name: "fade-down", Label: "Fade in from above"},
	{Name: "fade-left", Label: "Fade in from the right"},
	{Name: "fade-right", Label: "Fade in from the left"},
	{Name: "zoom-in", Label: "Zoom in"},
	{Name: "zoom-out", Label: "Zoom out"},
	{Name: "blur-in", Label: "Come into focus"},
	{Name: "rise", Label: "Rise"},
	{Name: "pop", Label: "Pop"},
	{Name: "flip-up", Label: "Flip up"},
	{Name: "tilt-in", Label: "Tilt in"},
	{Name: "reveal-up", Label: "Reveal upwards"},
	{Name: "reveal-right", Label: "Reveal left to right"},
	{Name: "reveal-circle", Label: "Reveal in a circle"},
	{Name: "float", Label: "Float (keeps moving)", Loop: true},
	{Name: "pulse", Label: "Pulse (keeps moving)", Loop: true},
}

// None is the value for no animation.
const None = "none"

// Levels of the site's motion (the customiser's Animations): "" is the
// normal one.
var Levels = []string{"", "subtle", "expressive", "off"}

var index = func() map[string]*Animation {
	m := make(map[string]*Animation, len(Animations))
	for i := range Animations {
		m[Animations[i].Name] = &Animations[i]
	}
	return m
}()

// Lookup returns the animation named name.
func Lookup(name string) (*Animation, bool) {
	a, ok := index[name]
	return a, ok
}

// Names returns None followed by every animation's name.
func Names() []string {
	out := []string{None}
	for _, a := range Animations {
		out = append(out, a.Name)
	}
	return out
}

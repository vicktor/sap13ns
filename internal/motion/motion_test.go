package motion

import (
	"os"
	"strings"
	"testing"
)

// Every animation offered has its keyframes and its selectors in the
// library's stylesheet, and every loop is marked as one there.
func TestLibraryHasEveryAnimation(t *testing.T) {
	b, err := os.ReadFile("../../ui/static/motion.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, a := range Animations {
		for _, want := range []string{
			"@keyframes sap-" + a.Name + " ",
			`[data-sap-animate="` + a.Name + `"], [data-sap-stagger="` + a.Name + `"] > * { --sap-anim: sap-` + a.Name + ";",
		} {
			if !strings.Contains(css, want) {
				t.Errorf("motion.css lacks %q", want)
			}
		}
		loop := strings.Contains(css, `[data-sap-stagger="`+a.Name+`"] > * { --sap-anim: sap-`+a.Name+"; --sap-iter: infinite;")
		if loop != a.Loop {
			t.Errorf("%s: loop %v in motion.css, %v here", a.Name, loop, a.Loop)
		}
	}
	if _, ok := Lookup(None); ok {
		t.Error("none is an animation")
	}
	if n := Names(); n[0] != None || len(n) != len(Animations)+1 {
		t.Errorf("Names = %v", n)
	}
}

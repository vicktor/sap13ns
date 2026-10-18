package theme

// Tokens are a theme's default colours for the customiser's public tokens
// (background, foreground, card, ... see customize.ColorTokens), as hex.
// They are what the customiser shows as "theme default" and what contrast
// checks compare against; the theme's CSS holds the same values as the
// fallbacks of var(--sap-<scheme>-<token>, <default>). Options lists the
// overrides an option value applies (an accent choice changes primary).
type Tokens struct {
	Light   map[string]string                    `json:"light"`
	Dark    map[string]string                    `json:"dark"`
	Options map[string]map[string]TokenOverrides `json:"options,omitempty"` // option key -> value -> overrides
}

// TokenOverrides are the colours an option value changes.
type TokenOverrides struct {
	Light map[string]string `json:"light,omitempty"`
	Dark  map[string]string `json:"dark,omitempty"`
}

// DefaultColors returns the effective default colours for the given option
// values (as returned by OptionValues).
func (t *Theme) DefaultColors(opts map[string]string) (light, dark map[string]string) {
	light, dark = map[string]string{}, map[string]string{}
	if t.Tokens == nil {
		return
	}
	for k, v := range t.Tokens.Light {
		light[k] = v
	}
	for k, v := range t.Tokens.Dark {
		dark[k] = v
	}
	for key, values := range t.Tokens.Options {
		o, ok := values[opts[key]]
		if !ok {
			continue
		}
		for k, v := range o.Light {
			light[k] = v
		}
		for k, v := range o.Dark {
			dark[k] = v
		}
	}
	return
}

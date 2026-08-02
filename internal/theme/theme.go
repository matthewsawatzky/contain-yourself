// Package theme resolves the controller's accent colour into a full palette.
//
// Resolution happens on the server rather than in CSS for two reasons: the
// foreground colour that sits on top of the accent has to be chosen by
// measuring contrast, which CSS cannot yet do portably; and the palette is
// published as JSON so app UIs running inside a workstation can match the
// controller without reimplementing any of this.
package theme

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// DefaultAccent is the deployment default when neither a workstation nor a
// user has chosen one.
const DefaultAccent = "#ff6b00"

// Presets are the swatches offered next to the colour picker.
var Presets = []Preset{
	{Name: "Orange", Value: "#ff6b00"},
	{Name: "Amber", Value: "#f5a524"},
	{Name: "Green", Value: "#22c55e"},
	{Name: "Teal", Value: "#14b8a6"},
	{Name: "Blue", Value: "#3ea6ff"},
	{Name: "Violet", Value: "#8b7cff"},
	{Name: "Pink", Value: "#ec4899"},
	{Name: "Red", Value: "#ef4444"},
}

type Preset struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Palette is the resolved theme. It is both rendered as CSS custom properties
// and served as JSON from the theme endpoint.
type Palette struct {
	Accent       string `json:"accent"`
	AccentStrong string `json:"accent_strong"`
	AccentMuted  string `json:"accent_muted"`
	AccentSoft   string `json:"accent_soft"`
	OnAccent     string `json:"on_accent"`
	Sidebar      string `json:"sidebar"`
	Background   string `json:"background"`
	Surface      string `json:"surface"`
	SurfaceRaise string `json:"surface_raised"`
	SurfaceSunk  string `json:"surface_sunk"`
	Line         string `json:"line"`
	LineStrong   string `json:"line_strong"`
	Text         string `json:"text"`
	Muted        string `json:"muted"`
	Success      string `json:"success"`
	Warning      string `json:"warning"`
	Danger       string `json:"danger"`
	// Source records where the accent came from: "workstation", "user", or
	// "default". App UIs can use it to decide whether to follow the theme.
	Source string `json:"source"`
}

var hexPattern = regexp.MustCompile(`^#?([0-9a-fA-F]{6})$`)

// Normalize validates a user-supplied colour and returns it in lowercase
// six-digit hex form. Only opaque hex is accepted: the value is interpolated
// into a stylesheet, so the input alphabet is kept deliberately narrow.
func Normalize(value string) (string, error) {
	match := hexPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return "", errors.New("accent colour must be a six-digit hex value such as #ff6b00")
	}
	return "#" + strings.ToLower(match[1]), nil
}

// Resolve picks the effective accent from the workstation override, then the
// user preference, then the deployment default, and expands it into a palette.
func Resolve(userAccent, workstationAccent string) Palette {
	accent, source := DefaultAccent, "default"
	if value, err := Normalize(userAccent); err == nil {
		accent, source = value, "user"
	}
	if value, err := Normalize(workstationAccent); err == nil {
		accent, source = value, "workstation"
	}
	return build(accent, source)
}

// The neutral scale is deliberately free of any blue cast, and the two border
// values are translucent rather than solid. A solid stroke reads as a box; a
// low-alpha one reads as a seam, which is most of what makes a dark interface
// look flat rather than boxed-in.
func build(accent, source string) Palette {
	r, g, b := parse(accent)
	return Palette{
		Accent:       accent,
		AccentStrong: hex(lighten(r, g, b, 0.18)),
		AccentMuted:  hex(mix(r, g, b, 0x18, 0x18, 0x18, 0.80)),
		AccentSoft:   hex(mix(r, g, b, 0x12, 0x12, 0x12, 0.90)),
		OnAccent:     onAccent(r, g, b),
		Sidebar:      "#0a0a0a",
		Background:   "#121212",
		Surface:      "#181818",
		SurfaceRaise: "#1f1f1f",
		SurfaceSunk:  "#0e0e0e",
		Line:         "rgba(148,148,158,.12)",
		LineStrong:   "rgba(148,148,158,.20)",
		Text:         "#fafafa",
		Muted:        "#9b9ba4",
		Success:      "#22c55e",
		Warning:      "#eab308",
		Danger:       "#ef4444",
		Source:       source,
	}
}

// onAccent returns the text colour to place on top of the accent. Bright
// accents such as orange need dark text; dark accents need light text.
// Choosing by measured contrast keeps primary buttons legible whatever colour
// the user picks.
func onAccent(r, g, b int) string {
	if contrast(luminance(r, g, b), luminance(0x0a, 0x08, 0x14)) >=
		contrast(luminance(r, g, b), luminance(0xff, 0xff, 0xff)) {
		return "#0a0814"
	}
	return "#ffffff"
}

func contrast(a, b float64) float64 {
	lighter, darker := math.Max(a, b), math.Min(a, b)
	return (lighter + 0.05) / (darker + 0.05)
}

// luminance is the WCAG relative luminance of an sRGB colour.
func luminance(r, g, b int) float64 {
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

func channel(value int) float64 {
	v := float64(value) / 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func lighten(r, g, b int, amount float64) (int, int, int) {
	return mix(r, g, b, 255, 255, 255, amount)
}

// mix blends the accent toward a target colour by amount, where 0 keeps the
// accent and 1 returns the target.
func mix(r, g, b, tr, tg, tb int, amount float64) (int, int, int) {
	blend := func(from, to int) int {
		return clamp(int(math.Round(float64(from) + (float64(to)-float64(from))*amount)))
	}
	return blend(r, tr), blend(g, tg), blend(b, tb)
}

func clamp(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}

func parse(value string) (int, int, int) {
	number, err := strconv.ParseUint(strings.TrimPrefix(value, "#"), 16, 32)
	if err != nil {
		return 0xff, 0x6b, 0x00
	}
	return int(number>>16) & 0xff, int(number>>8) & 0xff, int(number) & 0xff
}

func hex(r, g, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// CSS renders the palette as a custom-property block. The controller serves it
// as its own stylesheet so the page needs no inline styles and the strict
// style-src Content-Security-Policy stays intact.
func (p Palette) CSS() string {
	var out strings.Builder
	out.WriteString(":root{")
	for _, pair := range [][2]string{
		{"--accent", p.Accent},
		{"--accent-strong", p.AccentStrong},
		{"--accent-muted", p.AccentMuted},
		{"--accent-soft", p.AccentSoft},
		{"--on-accent", p.OnAccent},
		{"--sidebar", p.Sidebar},
		{"--bg", p.Background},
		{"--panel", p.Surface},
		{"--panel-raised", p.SurfaceRaise},
		{"--panel-soft", p.SurfaceSunk},
		{"--line", p.Line},
		{"--line-strong", p.LineStrong},
		{"--text", p.Text},
		{"--muted", p.Muted},
		{"--green", p.Success},
		{"--warning", p.Warning},
		{"--danger", p.Danger},
	} {
		out.WriteString(pair[0])
		out.WriteByte(':')
		out.WriteString(pair[1])
		out.WriteByte(';')
	}
	out.WriteString("}")
	out.WriteString(presetSwatchCSS())
	return out.String()
}

// presetSwatchCSS paints the picker's preset swatches. The colours have to
// reach the page somehow, and a strict style-src forbids the inline style
// attribute that would otherwise carry them, so they are emitted as rules.
func presetSwatchCSS() string {
	var out strings.Builder
	for _, preset := range Presets {
		value, err := Normalize(preset.Value)
		if err != nil {
			continue
		}
		out.WriteString(`.swatch[data-accent-preset="`)
		out.WriteString(value)
		out.WriteString(`"]{background:`)
		out.WriteString(value)
		out.WriteString(";}")
	}
	return out.String()
}

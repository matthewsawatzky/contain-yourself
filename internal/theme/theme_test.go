package theme

import (
	"strings"
	"testing"
)

func TestNormalizeAcceptsHexWithAndWithoutHash(t *testing.T) {
	for _, input := range []string{"#FF6B00", "ff6b00", "  #Ff6B00  "} {
		value, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", input, err)
		}
		if value != "#ff6b00" {
			t.Fatalf("Normalize(%q) = %q, want #ff6b00", input, value)
		}
	}
}

// The accent is interpolated straight into a stylesheet, so anything that
// could close the declaration or open a new rule must be rejected outright.
func TestNormalizeRejectsAnythingButSixDigitHex(t *testing.T) {
	for _, input := range []string{
		"", "red", "#fff", "#ff6b0", "#ff6b000", "rgb(255,0,0)",
		"#ff6b00;}body{display:none", "#ff6b00/**/", "#gggggg", "url(x)",
	} {
		if value, err := Normalize(input); err == nil {
			t.Fatalf("Normalize(%q) accepted and returned %q", input, value)
		}
	}
}

func TestResolvePrefersWorkstationThenUserThenDefault(t *testing.T) {
	if p := Resolve("", ""); p.Accent != DefaultAccent || p.Source != "default" {
		t.Fatalf("empty resolve = %q/%q", p.Accent, p.Source)
	}
	if p := Resolve("#3ea6ff", ""); p.Accent != "#3ea6ff" || p.Source != "user" {
		t.Fatalf("user resolve = %q/%q", p.Accent, p.Source)
	}
	if p := Resolve("#3ea6ff", "#22c55e"); p.Accent != "#22c55e" || p.Source != "workstation" {
		t.Fatalf("workstation resolve = %q/%q", p.Accent, p.Source)
	}
}

// A stored value that is somehow invalid must fall through to the next source
// rather than reaching the stylesheet.
func TestResolveIgnoresInvalidStoredValues(t *testing.T) {
	if p := Resolve("nonsense", ""); p.Accent != DefaultAccent || p.Source != "default" {
		t.Fatalf("invalid user accent leaked: %q", p.Accent)
	}
	if p := Resolve("#3ea6ff", "nonsense"); p.Accent != "#3ea6ff" || p.Source != "user" {
		t.Fatalf("invalid workstation accent leaked: %q", p.Accent)
	}
}

// Primary buttons paint text in --on-accent over --accent. Whatever colour the
// user picks, that pairing has to stay readable.
func TestOnAccentKeepsReadableContrast(t *testing.T) {
	for _, accent := range append([]string{"#ffffff", "#000000", "#708080"},
		presetValues()...) {
		palette := Resolve("", accent)
		r, g, b := parse(palette.Accent)
		fr, fg, fb := parse(palette.OnAccent)
		ratio := contrast(luminance(r, g, b), luminance(fr, fg, fb))
		// WCAG AA for large/bold text is 3:1; button labels are bold.
		if ratio < 3 {
			t.Errorf("accent %s against %s has contrast %.2f, want >= 3",
				palette.Accent, palette.OnAccent, ratio)
		}
	}
}

func TestBrightAccentsGetDarkTextAndDarkAccentsGetLight(t *testing.T) {
	if got := Resolve("", "#ff6b00").OnAccent; got != "#0a0814" {
		t.Fatalf("orange on-accent = %q, want dark text", got)
	}
	if got := Resolve("", "#1a1a5e").OnAccent; got != "#ffffff" {
		t.Fatalf("dark blue on-accent = %q, want light text", got)
	}
}

func TestCSSEmitsEveryVariableAndStaysInjectionFree(t *testing.T) {
	css := Resolve("", "#22c55e").CSS()
	for _, name := range []string{
		"--accent:", "--accent-strong:", "--accent-muted:", "--accent-soft:",
		"--on-accent:", "--bg:", "--panel:", "--line:", "--text:", "--muted:",
	} {
		if !strings.Contains(css, name) {
			t.Errorf("CSS is missing %s", name)
		}
	}
	// One :root block plus one rule per preset swatch, and nothing else.
	if want := 1 + len(Presets); strings.Count(css, "{") != want ||
		strings.Count(css, "}") != want {
		t.Fatalf("CSS should contain %d rules: %s", want, css)
	}
}

// A stored accent reaches the stylesheet as text, so a value that escaped
// validation could otherwise close the rule and inject arbitrary CSS.
func TestCSSCannotBeEscapedByAStoredAccent(t *testing.T) {
	css := Resolve("#ff6b00;}body{display:none;{", "").CSS()
	if strings.Contains(css, "display:none") {
		t.Fatalf("invalid accent reached the stylesheet: %s", css)
	}
	if !strings.Contains(css, "--accent:"+DefaultAccent) {
		t.Fatalf("rejected accent did not fall back to the default: %s", css)
	}
}

func TestPresetsAreValidAndUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, preset := range Presets {
		value, err := Normalize(preset.Value)
		if err != nil {
			t.Errorf("preset %s has an invalid value %q", preset.Name, preset.Value)
			continue
		}
		if seen[value] {
			t.Errorf("preset value %q is duplicated", value)
		}
		seen[value] = true
	}
}

func presetValues() []string {
	values := make([]string, 0, len(Presets))
	for _, preset := range Presets {
		values = append(values, preset.Value)
	}
	return values
}

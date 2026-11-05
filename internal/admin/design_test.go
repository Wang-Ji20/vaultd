package admin

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func cssText(t *testing.T) string {
	t.Helper()
	raw, err := webFiles.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCSSStaysSmall(t *testing.T) {
	if size := len(cssText(t)); size >= 20*1024 {
		t.Fatalf("app.css is %d bytes; the limit is 20 KB", size)
	}
}

type rgba struct{ r, g, b, a float64 }

func parseColor(t *testing.T, hex string) rgba {
	t.Helper()
	value := strings.TrimPrefix(hex, "#")
	if len(value) == 6 {
		value += "FF"
	}
	n, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		t.Fatalf("color %q: %v", hex, err)
	}
	return rgba{float64(n >> 24 & 255), float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n&255) / 255}
}

func luminance(c rgba) float64 {
	channel := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}

// contrast is the WCAG ratio of a possibly translucent color over a background.
func contrast(fg, bg rgba) float64 {
	over := rgba{fg.r*fg.a + bg.r*(1-fg.a), fg.g*fg.a + bg.g*(1-fg.a), fg.b*fg.a + bg.b*(1-fg.a), 1}
	a, b := luminance(over), luminance(bg)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

// tokens reads the variables of one theme's rule from the stylesheet.
func tokens(t *testing.T, selector string) map[string]string {
	t.Helper()
	css := cssText(t)
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("no rule %q", selector)
	}
	block := css[start:]
	block = block[:strings.Index(block, "color-scheme")]
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`--([a-z0-9-]+):(#[0-9A-Fa-f]+)`).FindAllStringSubmatch(block, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func TestColorsMeetWCAGAA(t *testing.T) {
	themes := map[string]string{"light": ":root{", "dark": ":root[data-theme=dark]{"}
	textPairs := [][2]string{
		{"label", "bg"}, {"label", "grouped"},
		{"text-2", "bg"}, {"text-2", "grouped"},
		{"accent-text", "bg"}, {"accent-text", "grouped"},
		{"success-text", "bg"}, {"success-text", "grouped"},
		{"warning-text", "bg"}, {"warning-text", "grouped"},
		{"danger-text", "bg"}, {"danger-text", "grouped"},
	}
	markPairs := [][2]string{{"accent", "grouped"}, {"control", "grouped"}}
	for theme, selector := range themes {
		values := tokens(t, selector)
		check := func(fg, bg string, need float64) {
			ratio := contrast(parseColor(t, values[fg]), parseColor(t, values[bg]))
			if ratio < need {
				t.Errorf("%s: %s on %s is %.2f, needs %.1f", theme, fg, bg, ratio, need)
			}
		}
		for _, pair := range textPairs {
			check(pair[0], pair[1], 4.5)
		}
		for _, pair := range markPairs {
			check(pair[0], pair[1], 3) // controls and marks need 3:1
		}
		white := parseColor(t, "#FFFFFF")
		if ratio := contrast(white, parseColor(t, values["accent-fill"])); ratio < 4.5 {
			t.Errorf("%s: white on accent-fill is %.2f", theme, ratio)
		}
	}
}

// The spec's colours stay in the stylesheet exactly as given.
func TestSpecColorsArePresent(t *testing.T) {
	want := map[string]map[string]string{
		":root{": {"bg": "#F2F2F7", "grouped": "#FFFFFF", "label": "#000000", "secondary": "#3C3C4399", "separator": "#3C3C434A",
			"accent": "#007AFF", "success": "#34C759", "warning": "#FF9500", "danger": "#FF3B30"},
		":root[data-theme=dark]{": {"bg": "#000000", "grouped": "#1C1C1E", "label": "#FFFFFF", "secondary": "#EBEBF599", "separator": "#54545899",
			"accent": "#0A84FF", "success": "#30D158", "warning": "#FF9F0A", "danger": "#FF453A"},
	}
	for selector, colors := range want {
		values := tokens(t, selector)
		for name, color := range colors {
			if values[name] != color {
				t.Errorf("%s --%s: got %s, want %s", selector, name, values[name], color)
			}
		}
	}
}

// The CSP forbids inline scripts and styles, so templates must not use them.
func TestTemplatesAreCSPClean(t *testing.T) {
	entries, err := webFiles.ReadDir("web")
	if err != nil {
		t.Fatal(err)
	}
	banned := regexp.MustCompile(`(?i)\sstyle=|\son[a-z]+=|hx-on|<style|<script(?:[^>]*)>[^<]|javascript:`)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		raw, err := webFiles.ReadFile("web/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if found := banned.Find(raw); found != nil {
			t.Errorf("%s: %q", entry.Name(), fmt.Sprintf("%s", found))
		}
	}
}

package app

import (
	"bytes"
	"image/png"
	"testing"
	"time"
)

// channel_image_test.go checks Chinese glyph availability, reference table surfaces,
// bounded image dimensions, and source-driven metric selection. The tests render
// embedded fonts locally and inspect the PNG without contacting Telegram or
// requiring operating-system font installation.

// TestStatusChart renders the reference layout with real embedded CJK glyphs.
// Twelve Chinese channel rows stay inside the preview dimension limit, and
// missing observations retain the unknown color and dash representations.
func TestStatusChart(t *testing.T) {
	f := setup(t)
	state := ChannelState{Updated: f.clock.Unix(), PoolAt: f.clock.Format(time.RFC3339)}
	for index := range 12 {
		state.Groups = append(state.Groups, Group{ID: int64(index + 1), Name: "【GPT】满血高缓不降智⭐️⭐️⭐️"})
	}
	raw, err := renderPNG(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Encoded = ""
	image, err := png.Decode(bytes.NewReader(raw))
	if err != nil || image.Bounds().Dx() != 1000 || image.Bounds().Dy() > 2000 {
		t.Fatal("invalid chart dimensions", err)
	}
	if image.At(0, 0) != chartPalette.Header || image.At(0, 165) != chartPalette.Band {
		t.Fatal("reference header and table surfaces missing")
	}
	regular, err := parsedChartFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, character := range "渠道运行状态可用账号满血特惠≥≤∞" {
		glyph, err := regular[0].GlyphIndex(nil, character)
		if err != nil || glyph == 0 {
			t.Fatalf("missing embedded Chinese glyph %c", character)
		}
	}
	for _, item := range []struct {
		rate float64 // observed success ratio
		want string  // expected percentage label
	}{{1, "100.0%"}, {.995, "99.5%"}, {0, "0.0%"}, {-1, "—"}, {1.1, "—"}} {
		if got := ratioLabel(&item.rate); got != item.want {
			t.Fatalf("ratio %v rendered %s", item.rate, got)
		}
	}
	if rateColor(nil) != chartPalette.Unknown {
		t.Fatal("missing observations are not gray")
	}
	window, bucket := historyWindow([]Group{{Availability: &Availability{Window: .0625, Bucket: 5}}})
	if window != "90 分钟" || bucket != 5 {
		t.Fatal("history mislabeled as days", window, bucket)
	}
}

// TestRecentMetrics retains independently observed latency and cache values.
// The newest valid bucket wins per metric, missing metrics use earlier buckets,
// and the window summary supplies values absent from the complete history.
func TestRecentMetrics(t *testing.T) {
	one, two := int64(1000), int64(2000)
	cache := .95
	value := &Availability{Health: &Health{TTFT: &two}, Days: []AvailabilityBucket{{Health: &Health{TTFT: &one}}, {Health: &Health{Cache: &cache}}, {}}}
	ttft, hit := recentMetrics(value)
	if ttft == nil || *ttft != 1000 || hit == nil || *hit != .95 {
		t.Fatal("recent metrics were associated with the wrong interval")
	}
	value.Days = nil
	ttft, hit = recentMetrics(value)
	if ttft == nil || *ttft != 2000 || hit != nil {
		t.Fatal("window summary was lost or fabricated a cache observation")
	}
}

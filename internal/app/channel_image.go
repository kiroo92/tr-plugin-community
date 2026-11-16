package app

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// channel_image.go renders a Chinese channel status table with historical
// success ratios, observed latency, and cache hit rates. Embedded CJK fonts keep
// the image independent of fonts installed on the host operating system. Row
// names and observation gaps remain visible in the generated PNG, which is shared by website previews and Telegram broadcasts.
//
// History colors describe the host's sample-gated success ratios; missing ratios
// remain gray even when raw request counts exist. Latency and cache metrics use
// their most recent valid interval observation, with the window summary supplying
// a value when no interval contains one. Pool account counts belong to the
// Telegram caption and share the refreshed snapshot with the picture.

// The embedded fonts, parsed glyph cache, and reference palette supply every
// chart render with platform-independent typography and consistent observation colors.
var (
	// chartFonts contains the redistributed CJK font subsets and their license text.
	//go:embed fonts/*.otf fonts/OFL.txt
	chartFonts embed.FS

	// parsedChartFonts shares immutable parsed tables; each render owns its sized faces.
	parsedChartFonts = sync.OnceValues(parseChartFonts)

	// chartPalette fixes the surfaces and observed-ratio categories used by every table.
	chartPalette = chartColors{
		Header: color.RGBA{31, 49, 46, 255}, Paper: color.RGBA{255, 255, 255, 255},
		Stripe: color.RGBA{246, 248, 247, 255}, Band: color.RGBA{236, 243, 239, 255},
		Ink: color.RGBA{39, 53, 50, 255}, Muted: color.RGBA{122, 138, 133, 255},
		Accent: color.RGBA{115, 219, 185, 255}, Green: color.RGBA{24, 139, 108, 255},
		Lime: color.RGBA{129, 170, 70, 255}, Amber: color.RGBA{211, 159, 43, 255},
		Red: color.RGBA{206, 102, 98, 255}, Unknown: color.RGBA{229, 234, 232, 255},
	}
)

// chartColors names the reference table surfaces and observed ratio categories.
type chartColors struct {
	Header  color.RGBA // dark title surface
	Paper   color.RGBA // primary row surface
	Stripe  color.RGBA // alternating row surface
	Band    color.RGBA // heading footer surface
	Ink     color.RGBA // primary text shade
	Muted   color.RGBA // secondary text shade
	Accent  color.RGBA // title accent shade
	Green   color.RGBA // highest success category
	Lime    color.RGBA // second success category
	Amber   color.RGBA // third success category
	Red     color.RGBA // lowest success category
	Unknown color.RGBA // missing observation category
}

// renderPNG draws the current channel snapshot with a fixed table width.
// Callers pass at most twelve public groups. Font faces belong to this render
// and are closed before returning; the shared parsed glyph tables stay immutable.
// History rows use the interval metadata supplied by the host, while absent
// metadata selects the documented ninety-minute, five-minute display defaults.
// Unknown values are drawn as dashes, and the caption supplies the separately
// collected account observations for the two requested GPT pools.
func renderPNG(state ChannelState) ([]byte, error) {
	fonts, err := parsedChartFonts()
	if err != nil {
		return nil, err
	}
	faces := map[int]font.Face{}
	for _, size := range []int{16, 18, 20, 22, 24, 26, 40} {
		source := fonts[0]
		if size >= 22 {
			source = fonts[1]
		}
		face, err := opentype.NewFace(source, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		faces[size] = face
		defer face.Close()
	}
	height := 220 + max(1, len(state.Groups))*104 + 118
	canvas := image.NewRGBA(image.Rect(0, 0, 1000, height))
	fill := func(x, y, width, height int, shade color.RGBA) {
		draw.Draw(canvas, image.Rect(x, y, x+width, y+height), &image.Uniform{shade}, image.Point{}, draw.Src)
	}
	text := func(x, baseline, size int, value string, shade color.RGBA) {
		writer := font.Drawer{Dst: canvas, Src: image.NewUniform(shade), Face: faces[size], Dot: fixed.P(x, baseline)}
		writer.DrawString(value)
	}
	center := func(x, baseline, size int, value string, shade color.RGBA) {
		width := font.MeasureString(faces[size], value).Ceil()
		text(x-width/2, baseline, size, value, shade)
	}
	window, bucket := historyWindow(state.Groups)
	fill(0, 0, 1000, height, chartPalette.Paper)
	fill(0, 0, 1000, 156, chartPalette.Header)
	fill(0, 156, 1000, 4, chartPalette.Green)
	text(48, 51, 22, "MOONUP / MODEL STATUS", chartPalette.Accent)
	text(48, 114, 40, "渠道运行状态", chartPalette.Paper)
	stamp := time.Unix(state.Updated, 0).In(time.FixedZone("UTC+8", 8*3600)).Format("2006/01/02 15:04 UTC+8")
	text(728, 51, 18, stamp, chartPalette.Accent)
	text(756, 112, 20, fmt.Sprintf("%d 个渠道 · 近 %s", len(state.Groups), window), chartPalette.Paper)
	fill(0, 160, 1000, 60, chartPalette.Band)
	text(48, 198, 18, "渠道 / 观测", chartPalette.Muted)
	text(354, 198, 18, fmt.Sprintf("%d 分钟可用率", bucket), chartPalette.Muted)
	for index, label := range []string{"可用率", "首字 P50", "缓存命中"} {
		center(740+index*100, 198, 18, label, chartPalette.Muted)
	}
	for index, group := range state.Groups {
		top := 220 + index*104
		if index%2 == 1 {
			fill(0, top, 1000, 104, chartPalette.Stripe)
		}
		name := chartName(group.Name)
		text(48, top+38, 22, fitText(faces[22], name, 290), chartPalette.Ink)
		status, shade := observationStatus(group.Availability)
		fill(49, top+69, 7, 7, shade)
		text(66, top+79, 16, status, chartPalette.Muted)
		if group.Availability != nil {
			buckets := group.Availability.Days
			// Use the host-provided interval sequence rather than interpreting its Days
			// field as calendar days; the current public window uses five-minute buckets.
			cellWidth := 290.0 / float64(max(1, len(buckets)))
			for position := 0; position < len(buckets); position++ {
				var rate *float64
				if position < len(buckets) {
					rate = buckets[position].Rate
				}
				left := 354 + int(float64(position)*cellWidth)
				width := max(1, int(cellWidth)-2)
				fill(left, top+39, width, 27, rateColor(rate))
			}
		} else {
			for position := range 18 {
				fill(354+position*16, top+39, 12, 27, chartPalette.Unknown)
			}
		}
		rate, latency, cache := "—", "—", "—"
		if availability := group.Availability; availability != nil {
			rate = ratioLabel(availability.Rate)
			ttft, hit := recentMetrics(availability)
			if ttft != nil && *ttft >= 0 {
				// Catalog latency uses milliseconds; chart labels use seconds.
				latency = fmt.Sprintf("%.1fs", float64(*ttft)/1000)
			}
			cache = ratioLabel(hit)
		}
		for column, value := range []string{rate, latency, cache} {
			shade := chartPalette.Ink
			if value == "—" {
				shade = chartPalette.Muted
			}
			center(740+column*100, top+62, 22, value, shade)
		}
	}
	if len(state.Groups) == 0 {
		text(48, 278, 22, "暂无公开渠道", chartPalette.Muted)
	}
	top := height - 118
	fill(0, top, 1000, 118, chartPalette.Band)
	for index, entry := range []struct {
		label string     // displayed ratio category
		shade color.RGBA // corresponding observation color
	}{{"≥99.5%", chartPalette.Green}, {"≥98%", chartPalette.Lime}, {"≥90%", chartPalette.Amber}, {"<90%", chartPalette.Red}, {"无观测", chartPalette.Unknown}} {
		left := 48 + index*145
		fill(left, top+24, 14, 14, entry.shade)
		text(left+23, top+38, 16, entry.label, chartPalette.Muted)
	}
	text(48, top+66, 16, "色块从左到右表示历史观测；首字和缓存取最近有效观测", chartPalette.Muted)
	text(48, top+95, 16, "账号数量见消息附文 · 仅列 GPT 满血和特惠", chartPalette.Muted)
	text(745, top+95, 16, "来源 moonup.dev/models", chartPalette.Muted)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// parseChartFonts loads the two embedded glyph tables for the shared lazy cache.
// Errors remain cached with the result so renders fail consistently when their
// build contains invalid font assets. Sized font faces are created per render.
func parseChartFonts() ([2]*opentype.Font, error) {
	var result [2]*opentype.Font
	for index, name := range []string{"Regular", "Bold"} {
		raw, err := chartFonts.ReadFile("fonts/CommunityChartSans-" + name + ".otf")
		if err != nil {
			return result, err
		}
		parsed, err := opentype.Parse(raw)
		if err != nil {
			return result, err
		}
		result[index] = parsed
	}
	return result, nil
}

// chartName removes decorative stars and emoji selectors from channel labels.
// Unsupported supplementary symbols become spaces so the fixed name column keeps
// the descriptive Chinese text legible in Telegram's raster preview.
func chartName(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\ufe0f' || character == '\u200d' || character == '⭐' || character == '★' {
			return -1
		}
		if unicode.IsControl(character) || character > 0xffff {
			return ' '
		}
		return character
	}, value)
}

// fitText truncates a label by measured glyph width with an ellipsis.
// Callers pass the exact rendering face so Chinese and Latin text share the
// same column constraint despite their different character widths.
func fitText(face font.Face, value string, width int) string {
	if font.MeasureString(face, value).Ceil() <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && font.MeasureString(face, string(runes)+"…").Ceil() > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// historyWindow describes the first available host reporting window and bucket size.
// Missing metadata uses ninety minutes and five-minute intervals, matching the
// host defaults; existing metadata stays distinct from the number of observed buckets.
func historyWindow(groups []Group) (string, int) {
	minutes, bucket := 90, 5
	for _, group := range groups {
		if group.Availability != nil && group.Availability.Window > 0 {
			// Fractional day windows retain their minute duration for display.
			minutes = int(math.Round(group.Availability.Window * 24 * 60))
			if group.Availability.Bucket > 0 {
				bucket = group.Availability.Bucket
			}
			break
		}
	}
	if minutes >= 1440 && minutes%1440 == 0 {
		return fmt.Sprintf("%d 天", minutes/1440), bucket
	}
	return fmt.Sprintf("%d 分钟", minutes), bucket
}

// observationStatus describes the supplied sample-gated health or legacy observation status.
// Unknown health stays unknown even when raw success totals are present, because
// those totals do not establish that the host's sampling threshold was met.
func observationStatus(value *Availability) (string, color.RGBA) {
	if value == nil {
		return "观测未知", chartPalette.Muted
	}
	status := value.Status
	if value.Health != nil {
		status = value.Health.Status
	}
	switch status {
	case "healthy", "success", "ok":
		return "运行正常", chartPalette.Green
	case "warning":
		return "运行波动", chartPalette.Amber
	case "critical", "failure", "failed", "error", "unavailable":
		return "运行异常", chartPalette.Red
	default:
		return "观测未知", chartPalette.Muted
	}
}

// ratioLabel formats a finite observed proportion as a percentage.
// Missing ratios and values outside zero through one display a dash, so malformed
// responses cannot appear as measured success or cache values.
func ratioLabel(value *float64) string {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1 {
		return "—"
	}
	// Public ratios are fractions; labels display percentage units.
	return fmt.Sprintf("%.1f%%", *value*100)
}

// rateColor selects the legend color for an observed success proportion.
// Unknown and invalid ratios use the missing-observation surface, while valid
// ratios follow the percentages printed in the image legend.
func rateColor(value *float64) color.RGBA {
	if ratioLabel(value) == "—" {
		return chartPalette.Unknown
	}
	switch {
	case *value >= .995:
		return chartPalette.Green
	case *value >= .98:
		return chartPalette.Lime
	case *value >= .9:
		return chartPalette.Amber
	default:
		return chartPalette.Red
	}
}

// recentMetrics selects the most recent valid latency and cache observations.
// Each metric advances independently because a bucket may observe just one of
// them. Window health supplies any metric absent from every history bucket.
//
// Bucket order comes from the public catalog and is oldest to newest. Traversal
// starts at the newest bucket and keeps the first valid value of each metric;
// missing or negative latency and invalid cache ratios leave that metric open
// for an earlier bucket or the final window-summary lookup.
func recentMetrics(value *Availability) (*int64, *float64) {
	var ttft *int64
	var cache *float64
	for index := len(value.Days) - 1; index >= -1; index-- {
		health := value.Health
		if index >= 0 {
			health = value.Days[index].Health
		}
		if health == nil {
			continue
		}
		if ttft == nil && health.TTFT != nil && *health.TTFT >= 0 {
			ttft = health.TTFT
		}
		if cache == nil && ratioLabel(health.Cache) != "—" {
			cache = health.Cache
		}
		if ttft != nil && cache != nil {
			break
		}
	}
	return ttft, cache
}

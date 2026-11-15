package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type Availability struct {
	Rate    *float64 `json:"availability_rate"`
	Status  string   `json:"last_status"`
	Checked string   `json:"last_checked_at"`
	Days    []struct {
		Rate *float64 `json:"availability_rate"`
	} `json:"days"`
}
type Group struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Brand        string        `json:"display_brand"`
	Availability *Availability `json:"availability"`
}
type ChannelState struct {
	Updated int64   `json:"updated"`
	Sent    int64   `json:"sent"`
	Attempt int64   `json:"attempt"`
	Message int64   `json:"message"`
	Encoded string  `json:"image,omitempty"`
	Groups  []Group `json:"groups"`
	Error   string  `json:"error"`
}

func (plugin *Plugin) refreshChannels(ctx context.Context) (ChannelState, error) {
	var state ChannelState
	if _, err := plugin.load(ctx, plugin.scoped("channel"), &state); err != nil {
		return state, err
	}
	raw, err := plugin.env.Host.Catalog.Models(ctx)
	var envelope struct {
		Code int     `json:"code"`
		Data []Group `json:"data"`
	}
	if err != nil || len(raw) > 8<<20 || json.Unmarshal(raw, &envelope) != nil || envelope.Code != 0 || envelope.Data == nil {
		state.Error = "无法获取公开渠道数据，保留上次成功结果"
		if saveErr := plugin.save(ctx, plugin.scoped("channel"), state); saveErr != nil {
			return state, saveErr
		}
		return state, errors.New(state.Error)
	}
	state.Groups = envelope.Data
	if len(state.Groups) > 12 {
		state.Groups = state.Groups[:12]
	}
	state.Updated = plugin.now().Unix()
	state.Error = ""
	picture, err := renderPNG(state)
	if err != nil {
		return state, err
	}
	state.Encoded = base64.StdEncoding.EncodeToString(picture)
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return state, err
	}
	return state, nil
}

func renderPNG(state ChannelState) ([]byte, error) {
	height := 175 + len(state.Groups)*62
	if height < 250 {
		height = 250
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 900, height))
	background := color.RGBA{15, 23, 42, 255}
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{background}, image.Point{}, draw.Src)
	text := func(x, y int, label string, shade color.Color) {
		writer := font.Drawer{Dst: canvas, Src: image.NewUniform(shade), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
		writer.DrawString(label)
	}
	white := color.RGBA{226, 232, 240, 255}
	muted := color.RGBA{148, 163, 184, 255}
	green := color.RGBA{34, 197, 149, 255}
	amber := color.RGBA{234, 179, 8, 255}
	red := color.RGBA{239, 83, 80, 255}
	gray := color.RGBA{51, 65, 85, 255}
	draw.Draw(canvas, image.Rect(0, 0, 900, 5), &image.Uniform{color.RGBA{6, 182, 212, 255}}, image.Point{}, draw.Src)
	text(30, 38, "TOKENROUTER / PUBLIC CHANNEL STATUS", white)
	text(30, 64, "Fetched: "+time.Unix(state.Updated, 0).UTC().Format("2006-01-02 15:04:05 UTC"), muted)
	text(30, 100, "PUBLIC GROUP / BRAND", muted)
	text(365, 100, "RECENT DAYS (OBSERVED)", muted)
	text(665, 100, "WINDOW RATE", muted)
	text(785, 100, "LATEST", muted)
	for index, group := range state.Groups {
		top := 122 + index*62
		brand := group.Brand
		if brand == "" {
			brand = "Group"
		}
		brand = strings.Map(func(character rune) rune {
			if character < 32 || character > 126 {
				return '-'
			}
			return character
		}, brand)
		if len(brand) > 25 {
			brand = brand[:25]
		}
		text(30, top+17, fmt.Sprintf("#%d %s", group.ID, brand), white)
		rateLabel, statusLabel := "NO DATA", "UNKNOWN"
		statusColor := muted
		if group.Availability != nil {
			availability := group.Availability
			days := availability.Days
			if len(days) > 14 {
				days = days[len(days)-14:]
			}
			for dayIndex, day := range days {
				shade := gray
				if day.Rate != nil {
					shade = green
					if *day.Rate < 1 {
						shade = amber
					}
					if *day.Rate == 0 {
						shade = red
					}
				}
				left := 365 + dayIndex*18
				draw.Draw(canvas, image.Rect(left, top, left+12, top+24), &image.Uniform{shade}, image.Point{}, draw.Src)
			}
			if availability.Rate != nil {
				rateLabel = fmt.Sprintf("%.1f%%", *availability.Rate*100)
			}
			switch availability.Status {
			case "success", "healthy", "ok":
				statusLabel = "OK"
				statusColor = green
			case "failure", "failed", "error", "unavailable":
				statusLabel = "FAIL"
				statusColor = red
			}
			if availability.Checked != "" {
				checked := []rune(availability.Checked)
				if len(checked) > 32 {
					checked = checked[:32]
				}
				text(30, top+36, string(checked), muted)
			}
		}
		text(665, top+17, rateLabel, white)
		text(785, top+17, statusLabel, statusColor)
	}
	if len(state.Groups) == 0 {
		text(30, 140, "No public groups available.", muted)
	}
	text(30, height-25, "GREEN = SUCCESS   AMBER = PARTIAL   RED = FAILURE   GRAY = NO DATA", muted)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (plugin *Plugin) broadcast(ctx context.Context, manual bool) error {
	if !plugin.settings.Channels {
		return errors.New("渠道播报已暂停")
	}
	var state ChannelState
	if _, err := plugin.load(ctx, plugin.scoped("channel"), &state); err != nil {
		return err
	}
	wait := int64(plugin.settings.Cooldown)
	if !manual {
		wait = int64(plugin.settings.Interval * 60)
	}
	if state.Attempt > 0 && plugin.now().Unix()-state.Attempt < wait {
		if manual {
			return errors.New("查询冷却中，请稍后重试")
		}
		return nil
	}
	state.Attempt = plugin.now().Unix()
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return err
	}
	state, err := plugin.refreshChannels(ctx)
	if err != nil {
		return err
	}
	picture, err := base64.StdEncoding.DecodeString(state.Encoded)
	if err != nil {
		return errors.New("状态图损坏")
	}
	caption := "网站公开渠道探测 · " + time.Unix(state.Updated, 0).In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04:05 UTC+8") + "\n"
	for _, group := range state.Groups {
		name := []rune(group.Name)
		if len(name) > 40 {
			name = name[:40]
		}
		caption += fmt.Sprintf("#%d %s\n", group.ID, string(name))
	}
	caption += "图中按组 ID 对应名称；灰色代表暂无数据。最多展示前 12 个公开分组。"
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	method := "sendPhoto"
	if state.Message > 0 {
		method = "editMessageMedia"
		_ = writer.WriteField("message_id", strconv.FormatInt(state.Message, 10))
		media, _ := json.Marshal(map[string]string{"type": "photo", "media": "attach://photo", "caption": caption})
		_ = writer.WriteField("media", string(media))
	} else {
		_ = writer.WriteField("caption", caption)
	}
	_ = writer.WriteField("chat_id", plugin.chat)
	part, err := writer.CreateFormFile("photo", "channels.png")
	if err != nil {
		return err
	}
	if _, err := part.Write(picture); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, plugin.telegramURL+"/bot"+plugin.token+"/"+method, &body)
	if err != nil {
		return errors.New("创建图片请求失败")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	var retry int64
	if _, err := plugin.load(ctx, plugin.scoped("retry"), &retry); err != nil {
		return err
	}
	if retry > plugin.now().Unix() {
		return errors.New("Telegram 请求冷却中")
	}
	response, err := plugin.client.Do(request)
	if err != nil {
		return errors.New("状态图发送结果未确认，请稍后检查群内消息")
	}
	defer response.Body.Close()
	var result struct {
		ID int64 `json:"message_id"`
	}
	if err := plugin.telegramResult(ctx, method, response, &result); err != nil {
		return err
	}
	if result.ID <= 0 {
		return errors.New("Telegram 未返回状态消息 ID")
	}
	state.Message = result.ID
	state.Sent = plugin.now().Unix()
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return err
	}
	return plugin.event(ctx, "渠道状态图已更新", "已更新群内消息；数据仅来自宿主公开目录。")
}

func decodeImage(state ChannelState) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(state.Encoded)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("暂无状态图，请管理员刷新")
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width > 1000 || config.Height > 1200 {
		return nil, errors.New("状态图格式无效")
	}
	return raw, nil
}

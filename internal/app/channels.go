package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"mime/multipart"
	"net/http"
	"strconv"
)

// channels.go refreshes public channel observations and publishes the rendered
// status image to the configured Telegram group. Public catalog records retain
// their reporting window and bucket metadata so rendering describes the actual
// observation interval. Optional PoolScope counts join by local group identity;
// absent counts stay unknown while catalog observations remain available.
//
// Refresh constructs a complete replacement image before saving it. Catalog
// failures retain the last successful image, while independent pool failures
// publish the current catalog with account counts marked unavailable. Broadcast
// persists its attempt before external delivery, then saves the returned message
// identifier so later updates edit the same group message under one cooldown.

// Health contains sample-gated latency and cache observations from the host.
type Health struct {
	Status string   `json:"status"`      // observed health category
	TTFT   *int64   `json:"ttft_p50_ms"` // milliseconds
	Cache  *float64 `json:"cache_rate"`  // observed cache ratio
}

// AvailabilityBucket describes one ordered interval in the host's reporting window.
type AvailabilityBucket struct {
	Date   string   `json:"date"`              // interval start timestamp
	Rate   *float64 `json:"availability_rate"` // observed success ratio
	Health *Health  `json:"health"`            // optional interval health
}

// Availability preserves the reporting interval and sample-gated channel observations.
type Availability struct {
	Rate    *float64             `json:"availability_rate"` // observed success ratio
	Status  string               `json:"last_status"`       // most recent status
	Checked string               `json:"last_checked_at"`   // latest observation timestamp
	Window  float64              `json:"window_days"`       // fractional days
	Bucket  int                  `json:"bucket_minutes"`    // minutes per interval
	Days    []AvailabilityBucket `json:"days"`              // ordered history intervals
	Health  *Health              `json:"health"`            // optional window health
}

// Group combines one public channel with its optional available account observation.
type Group struct {
	ID           int64         `json:"id"`            // local group identifier
	Name         string        `json:"name"`          // public group name
	Brand        string        `json:"display_brand"` // public brand label
	Availability *Availability `json:"availability"`  // optional catalog observations
	// Available is populated only by a successful PoolScope response matching ID.
	// A nil value represents unknown statistics; a pointer to zero is measured zero.
	Available *int64 `json:"available_accounts"`
}

// ChannelState persists the current chart, delivery identity, and source freshness.
type ChannelState struct {
	Updated   int64   `json:"updated"`         // catalog retrieval Unix seconds
	Sent      int64   `json:"sent"`            // successful delivery Unix seconds
	Attempt   int64   `json:"attempt"`         // delivery attempt Unix seconds
	Message   int64   `json:"message"`         // edited Telegram message identifier
	Encoded   string  `json:"image,omitempty"` // base64 PNG
	Groups    []Group `json:"groups"`          // displayed public groups
	Error     string  `json:"error"`           // catalog refresh failure
	PoolError string  `json:"pool_error"`      // pool retrieval failure
	PoolAt    string  `json:"pool_at"`         // pool snapshot timestamp
	PoolStale bool    `json:"pool_stale"`      // prior snapshot warning
}

// refreshChannels creates a replacement chart from current public observations.
// The caller holds plugin.mu. Catalog failure records its diagnostic while
// retaining the last successful chart; pool failure is independent and clears
// account counts before rendering to distinguish unavailable counts from zero.
// Source records are limited to twelve visible public groups, and image encoding
// finishes before the complete chart state is saved for preview and delivery.
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
	// A failed catalog refresh retains the complete saved image and its observations.
	// Independent pool retrieval runs only after a valid current catalog has been accepted.
	if err != nil || len(raw) > 8<<20 || json.Unmarshal(raw, &envelope) != nil || envelope.Code != 0 || envelope.Data == nil {
		state.Error = "无法获取公开渠道数据，保留上次成功结果"
		if saveErr := plugin.save(ctx, plugin.scoped("channel"), state); saveErr != nil {
			return state, saveErr
		}
		return state, errors.New(state.Error)
	}
	state.Groups = nil
	for _, group := range envelope.Data {
		// Statistics are populated by the separate pool response. Discard any count
		// fields in catalog JSON so mixed schemas cannot bypass identity matching.
		group.Available = nil
		state.Groups = append(state.Groups, group)
	}
	if len(state.Groups) > 12 {
		// Bound picture height and Telegram caption size for group delivery.
		state.Groups = state.Groups[:12]
	}
	state.Updated = plugin.now().Unix()
	state.Error = ""
	plugin.loadPoolCounts(ctx, &state)
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

// broadcast refreshes and sends the channel chart under the shared group cooldown.
// Callers hold plugin.mu. Manual requests use Cooldown seconds, while scheduled
// requests use the configured interval. A persisted attempt reserves the cooldown
// before catalog access or Telegram delivery because a failed response can leave
// the remote outcome uncertain. Successful delivery records Telegram's message
// identifier for subsequent edits; failed edits return an error so callers can
// inspect the existing message before another attempt.
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
	// Reserve the shared cooldown before sending. An interrupted request may already
	// have created the remote photo, so immediate retries could duplicate group messages.
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
	caption := channelCaption(state)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	method := "sendPhoto"
	// Reuse the saved Telegram message identity after the first photo. Editing errors
	// propagate so a missing message does not silently cause a second broadcast.
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
	// Publish delivery identity after Telegram confirms it, so later requests edit the
	// accepted photo and observe the same successful-send timestamp across plugin restarts.
	state.Message = result.ID
	state.Sent = plugin.now().Unix()
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return err
	}
	return plugin.event(ctx, "渠道状态图已更新", "已更新群内消息；数据仅来自宿主公开目录。")
}

// decodeImage validates a stored PNG before returning its decoded bytes.
// Missing or invalid image data returns a user-facing error. The dimensions are
// bounded to the twelve-row chart layout before callers serialize a preview.
func decodeImage(state ChannelState) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(state.Encoded)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("暂无状态图，请管理员刷新")
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width > 1000 || config.Height > 2000 {
		return nil, errors.New("状态图格式无效")
	}
	return raw, nil
}

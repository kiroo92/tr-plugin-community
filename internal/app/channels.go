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
	"strings"
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
// identifier and superseded photo IDs before deleting the old messages. Failed
// cleanup stays in the stored queue and is retried under a separate cooldown.

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
	Message   int64   `json:"message"`         // current Telegram photo identifier
	Encoded   string  `json:"image,omitempty"` // base64 PNG
	Groups    []Group `json:"groups"`          // displayed public groups
	Error     string  `json:"error"`           // catalog refresh failure
	PoolError string  `json:"pool_error"`      // pool retrieval failure
	PoolAt    string  `json:"pool_at"`         // pool snapshot timestamp
	PoolStale bool    `json:"pool_stale"`      // prior snapshot warning
	// Cleanup stores superseded message identifiers after a replacement is saved.
	// Only these older photos may be deleted; Message identifies the current photo.
	Cleanup []int64 `json:"cleanup"`
	// DeleteAttempt reserves the cleanup retry interval before external deletion so
	// a remote failure does not cause a deletion request every polling tick.
	DeleteAttempt int64 `json:"delete_attempt"` // cleanup attempt Unix seconds
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
// identifier before deleting its predecessor. The cleanup queue survives restarts,
// and failed deletions remain queued while the new photo stays available.
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
	_ = writer.WriteField("caption", caption)
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, plugin.telegramURL+"/bot"+plugin.token+"/sendPhoto", &body)
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
	if err := plugin.telegramResult(ctx, "sendPhoto", response, &result); err != nil {
		return err
	}
	if result.ID <= 0 {
		return errors.New("Telegram 未返回状态消息 ID")
	}
	// Commit the replacement identity and predecessor together before cleanup. A failed
	// save leaves the previous photo in place even if Telegram accepted the new one.
	if state.Message > 0 && state.Message != result.ID {
		if len(state.Cleanup) == 0 {
			// A completed queue starts a new replacement cycle. Its first deletion can run
			// immediately, while retained failures continue to honor the saved retry interval.
			state.DeleteAttempt = 0
		}
		state.Cleanup = append(state.Cleanup, state.Message)
	}
	state.Message = result.ID
	state.Sent = plugin.now().Unix()
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return err
	}
	if err := plugin.cleanupChannelMessages(ctx, &state); err != nil {
		// The new photo is already confirmed and saved. Record cleanup failure while
		// keeping delivery successful and its predecessor queued for a later polling attempt.
		if eventErr := plugin.event(ctx, "删除旧状态图失败", err.Error()); eventErr != nil {
			return eventErr
		}
	}
	detail := "已发送新的状态图。"
	if len(state.Cleanup) > 0 {
		detail += "旧消息正在等待清理。"
	}
	return plugin.event(ctx, "渠道状态图已发送", detail)
}

// retryChannelCleanup resumes queued photo deletions independently of broadcasting.
// Callers hold plugin.mu. Cleanup continues when broadcasts are paused so a
// previously replaced message can be removed after a restart or Telegram outage.
func (plugin *Plugin) retryChannelCleanup(ctx context.Context) error {
	var state ChannelState
	if _, err := plugin.load(ctx, plugin.scoped("channel"), &state); err != nil {
		return err
	}
	return plugin.cleanupChannelMessages(ctx, &state)
}

// cleanupChannelMessages deletes superseded photos after their replacements are saved.
// The caller holds plugin.mu and supplies the current persisted state. Failed
// requests retain the remaining queue, and a one-minute retry reservation limits
// repeated failures across polling ticks and process restarts.
//
// A saved current message is excluded from cleanup even if a malformed queue
// names it. Telegram's definite missing-message response completes cleanup because
// a prior successful deletion may have lost its response. Each completed deletion
// is persisted before the next request so a later failure preserves earlier progress.
func (plugin *Plugin) cleanupChannelMessages(ctx context.Context, state *ChannelState) error {
	if len(state.Cleanup) == 0 {
		return nil
	}
	if state.DeleteAttempt > 0 && plugin.now().Unix()-state.DeleteAttempt < 60 {
		return nil
	}
	// Persist retry timing before Telegram calls. Lost responses and rate limits must
	// not cause every five-second poll to repeat the same pending deletion immediately.
	state.DeleteAttempt = plugin.now().Unix()
	if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
		return err
	}
	for len(state.Cleanup) > 0 {
		id := state.Cleanup[0]
		if id > 0 && id != state.Message {
			var deleted bool
			err := plugin.telegram(ctx, "deleteMessage", map[string]any{"chat_id": plugin.chat, "message_id": id}, &deleted)
			// Telegram may report absence after an earlier deletion lost its response. Only
			// that validated refusal completes cleanup; other errors preserve the queued identifier.
			var rejection *telegramRejection
			missing := errors.As(err, &rejection) && rejection.code == 400 && strings.Contains(strings.ToLower(rejection.description), "message to delete not found")
			if err != nil && !missing {
				return err
			}
			if err == nil && !deleted {
				return errors.New("Telegram 未确认旧状态图已删除")
			}
		}
		// Persist the remaining queue after each completion. A later deletion failure
		// then retains just its unfinished identifiers for recovery after the next restart.
		state.Cleanup = state.Cleanup[1:]
		if err := plugin.save(ctx, plugin.scoped("channel"), state); err != nil {
			return err
		}
	}
	return nil
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

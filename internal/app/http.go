package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
)

// http.go exposes authenticated community status, invitation claims, and
// administrator controls through the plugin's declared routes. Host caller
// metadata determines user and administrator access. Responses reveal usable
// invitation URLs to eligible claimants, while administrator lists omit URLs and
// status reads hide ordinary links that require replacement after an upgrade.
//
// Each request attempts the same mutex used by polling. Busy requests return a
// conflict so external calls and invitation mutations run in one sequence.
// Settings are persisted before publication in memory, and diagnostics follow
// successful mutations. Image delivery requires eligibility or administrator
// access and returns encoded image bytes through the authenticated plugin API.

func problem(writer http.ResponseWriter, status int, err error) {
	sdk.WriteJSON(writer, status, map[string]string{"message": err.Error()})
}
func (plugin *Plugin) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range []string{"GET /status", "POST /invite", "GET /image", "GET /admin/status", "GET /admin/invites", "POST /admin/settings", "POST /admin/check", "POST /admin/revoke", "POST /admin/clear", "POST /admin/refresh", "POST /admin/send"} {
		mux.HandleFunc(route, plugin.serve)
	}
	return mux
}

// serve dispatches a declared community route after authenticating its caller.
// Administrator paths require the host's admin role. Busy requests return a
// conflict so callers can retry after polling or another HTTP request finishes.
//
// The shared mutex covers stored claims and configuration throughout dispatch.
// Status responses hide unusable and legacy invitation URLs, letting the user
// request a replacement while issue retains the stored URL needed for revocation.
func (plugin *Plugin) serve(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	caller, ok := sdk.CallerFrom(request.Context())
	if !ok || caller.UserID <= 0 {
		problem(writer, 401, errors.New("请先登录"))
		return
	}
	admin := len(request.URL.Path) > 7 && request.URL.Path[:7] == "/admin/"
	if admin && caller.Role != "admin" {
		problem(writer, 403, errors.New("需要管理员权限"))
		return
	}
	// Polling also holds this mutex across external calls. Reject busy HTTP
	// requests so invitation consumption and replacement cannot interleave with approval.
	if !plugin.mu.TryLock() {
		problem(writer, 409, errors.New("正在处理社群请求，请稍后重试"))
		return
	}
	defer plugin.mu.Unlock()
	if plugin.db == nil {
		problem(writer, 503, errors.New("插件已停止"))
		return
	}
	ctx := request.Context()
	switch request.URL.Path {
	case "/status", "/invite":
		var group GroupState
		if _, err := plugin.load(ctx, plugin.scoped("group"), &group); err != nil {
			problem(writer, 500, err)
			return
		}
		qualification, err := plugin.eligible(ctx, caller.UserID)
		if err != nil {
			problem(writer, 502, err)
			return
		}
		source := qualification.Source
		var current Invite
		if _, err := plugin.load(ctx, plugin.inviteKey(caller.UserID), &current); err != nil {
			problem(writer, 500, err)
			return
		}
		if request.URL.Path == "/invite" {
			if !plugin.settings.Invites || plugin.token == "" {
				problem(writer, 503, errors.New("管理员尚未开放邀请领取"))
				return
			}
			if source == "" {
				problem(writer, 403, errors.New("请先绑定 LinuxDO 或完成余额充值"))
				return
			}
			current, err = plugin.issue(ctx, caller.UserID, source)
			if err != nil {
				problem(writer, 409, err)
				return
			}
		}
		// Hide ordinary links from earlier versions so the page offers replacement.
		// Keep their stored URLs available to issue for revocation before replacement.
		if source == "" || !plugin.settings.Invites || !current.JoinRequest ||
			current.Used || current.Revoked || current.Revoke ||
			current.Expires <= plugin.now().Unix() {
			current.URL = ""
		}
		sdk.WriteJSON(writer, 200, map[string]any{"eligible": source != "", "source": source, "linuxdo": qualification.LinuxDO, "recharge": qualification.Recharge, "enabled": plugin.settings.Invites && plugin.token != "", "group": plugin.settings.Group, "invite": current, "ai": plugin.settings.AI, "name": plugin.settings.Name, "channels": plugin.settings.Channels, "keywords": plugin.settings.Keywords, "public": group.Public, "approval": group.Approval, "now": plugin.now().Unix()})
	case "/image":
		if caller.Role != "admin" {
			qualification, err := plugin.eligible(ctx, caller.UserID)
			if err != nil || qualification.Source == "" {
				problem(writer, 403, errors.New("需满足社群资格后查看"))
				return
			}
		}
		var state ChannelState
		if _, err := plugin.load(ctx, plugin.scoped("channel"), &state); err != nil {
			problem(writer, 500, err)
			return
		}
		raw, err := decodeImage(state)
		if err != nil {
			problem(writer, 404, err)
			return
		}
		sdk.WriteJSON(writer, 200, map[string]string{"image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)})
	case "/admin/status":
		var events []Event
		var bot Bot
		var channel ChannelState
		var group GroupState
		for key, target := range map[string]any{"events": &events, "bot": &bot, "channel": &channel, "group": &group} {
			if _, err := plugin.load(ctx, plugin.scoped(key), target); err != nil {
				problem(writer, 500, err)
				return
			}
		}
		usage, err := plugin.usage(ctx)
		if err != nil {
			problem(writer, 500, err)
			return
		}
		channel.Encoded = ""
		sdk.WriteJSON(writer, 200, map[string]any{"settings": plugin.settings, "configured": plugin.token != "", "ai_key_set": plugin.key != "", "bot": bot, "group": group, "channel": channel, "usage": usage, "events": events})
	case "/admin/invites":
		after, err := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
		if request.URL.Query().Get("after") == "" {
			after = 0
			err = nil
		}
		if err != nil || after < 0 {
			problem(writer, 400, errors.New("分页参数无效"))
			return
		}
		records, err := plugin.invites(ctx, after)
		if err != nil {
			problem(writer, 500, errors.New("邀请记录读取失败"))
			return
		}
		next := int64(0)
		if len(records) > 50 {
			// The extra record signals another page of invitations.
			records = records[:50]
			next = records[len(records)-1].UserID
		}
		for index := range records {
			records[index].URL = ""
		}
		sdk.WriteJSON(writer, 200, map[string]any{"records": records, "next": next})
	case "/admin/settings":
		var input Settings
		if err := readJSON(writer, request, &input); err != nil {
			problem(writer, 400, err)
			return
		}
		if err := input.validate(); err != nil {
			problem(writer, 400, err)
			return
		}
		// Publish settings after persistence so a failed save leaves the running
		// plugin consistent with the configuration that will be loaded after restart.
		if err := plugin.save(ctx, "settings", input); err != nil {
			problem(writer, 500, err)
			return
		}
		plugin.settings = input
		// Settings are already committed and published. Event storage failure does
		// not reverse the configuration or turn a completed update into a retry.
		_ = plugin.event(ctx, "社群配置已保存", "邀请、助手与播报设置已生效。")
		sdk.WriteJSON(writer, 200, map[string]bool{"saved": true})
	case "/admin/revoke":
		var input struct {
			UserID int64 `json:"user_id"`
		}
		if readJSON(writer, request, &input) != nil || input.UserID <= 0 {
			problem(writer, 400, errors.New("账号参数无效"))
			return
		}
		var current Invite
		found, err := plugin.load(ctx, plugin.inviteKey(input.UserID), &current)
		if err != nil || !found {
			problem(writer, 404, errors.New("未找到邀请"))
			return
		}
		if err := plugin.revoke(ctx, &current); err != nil {
			problem(writer, 502, err)
			return
		}
		sdk.WriteJSON(writer, 200, map[string]bool{"revoked": true})
	case "/admin/clear":
		if _, err := plugin.db.ExecContext(ctx, "DELETE FROM records WHERE key LIKE ?", plugin.scoped("history/%")); err != nil {
			problem(writer, 500, errors.New("清理上下文失败"))
			return
		}
		// Context deletion has already committed. Its diagnostic event can fail
		// independently because repeating the deletion does not restore conversation content.
		_ = plugin.event(ctx, "已清理 AI 上下文", "已删除当前群组保存的对话正文。")
		sdk.WriteJSON(writer, 200, map[string]bool{"cleared": true})
	case "/admin/check":
		plugin.action(writer, ctx, plugin.checkBot)
	case "/admin/refresh":
		plugin.action(writer, ctx, func(ctx context.Context) error { _, err := plugin.refreshChannels(ctx); return err })
	case "/admin/send":
		plugin.action(writer, ctx, func(ctx context.Context) error {
			if err := plugin.checkBot(ctx); err != nil {
				return err
			}
			return plugin.broadcast(ctx, true)
		})
	}
}

func readJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errors.New("请求 JSON 无效或超过大小限制")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
func (plugin *Plugin) action(writer http.ResponseWriter, ctx context.Context, action func(context.Context) error) {
	if err := action(ctx); err != nil {
		problem(writer, 502, err)
		return
	}
	sdk.WriteJSON(writer, 200, map[string]bool{"ok": true})
}

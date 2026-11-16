package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"
)

// binding_api.go answers administrator integrations that need to know whether
// a website account has a Telegram binding in the configured bot and group.
// The public route authenticates a dedicated secret header because the host
// forwards X-Plugin headers to plugins while retaining website authentication
// headers for its own middleware. Successful responses contain a Chinese yes
// or no value as plain text.
//
// Authentication completes before input validation or storage access. Queries
// use the same scoped invitation records as private Start redemption, under the
// polling mutex, so a concurrent binding or configuration change cannot split
// the queried scope from the observed record. Entry revocation keeps ownership
// recorded and therefore does not change an existing binding to an absent one.

// serveBinding authenticates an independent administrator key and reports binding presence.
// The query requires one positive user_id. Callers need no website session;
// authentication uses X-Plugin-Admin-Key and the configured binding_api_key secret.
//
// Secrets shorter than thirty-two characters disable the endpoint. Equal-size SHA-256
// digests are compared in constant time before the handler tries the shared
// plugin mutex. Busy or unavailable storage returns an error status, keeping a
// failed lookup distinct from the successful negative result returned for an
// account whose scoped invitation record has no Telegram identity.
func (plugin *Plugin) serveBinding(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	key := plugin.env.Config.Secret("binding_api_key")
	// The configured key serves as this endpoint's independent credential. Empty or
	// undersized configuration disables queries before any account records are inspected.
	if utf8.RuneCountInString(key) < 32 {
		http.Error(writer, "绑定查询密钥未配置（至少 32 字符）", http.StatusServiceUnavailable)
		return
	}
	values := request.Header.Values("X-Plugin-Admin-Key")
	if len(values) != 1 {
		http.Error(writer, "未授权", http.StatusUnauthorized)
		return
	}
	want := sha256.Sum256([]byte(key))
	got := sha256.Sum256([]byte(values[0]))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		http.Error(writer, "未授权", http.StatusUnauthorized)
		return
	}
	ids := request.URL.Query()["user_id"]
	if len(ids) != 1 {
		http.Error(writer, "请提供一个有效的网站用户 ID", http.StatusBadRequest)
		return
	}
	userID, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil || userID <= 0 {
		http.Error(writer, "网站用户 ID 无效", http.StatusBadRequest)
		return
	}
	if !plugin.mu.TryLock() {
		http.Error(writer, "正在处理社群请求，请稍后重试", http.StatusConflict)
		return
	}
	defer plugin.mu.Unlock()
	if plugin.db == nil || plugin.token == "" {
		http.Error(writer, "社群插件暂不可用", http.StatusServiceUnavailable)
		return
	}
	var current Invite
	if _, err := plugin.load(request.Context(), plugin.inviteKey(userID), &current); err != nil {
		http.Error(writer, "无法查询绑定状态", http.StatusInternalServerError)
		return
	}
	answer := "否"
	if current.TelegramID > 0 {
		answer = "是"
	}
	_, _ = io.WriteString(writer, answer)
}

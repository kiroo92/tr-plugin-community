package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// pool_counts.go reads the administrator-selected public PoolScope export and
// matches account counts to catalog groups by numeric identity. Pool responses
// carry their own snapshot timestamp because remote account synchronization and
// public channel observations advance independently. Failed or malformed exports
// leave every count unknown in the current chart.
//
// Telegram captions append the requested counts after the channel observation summary.
// Display labels may be shortened for the two GPT pools, while numeric identity
// remains the basis for association. Source timestamps and stale warnings travel
// with displayed observations, so a retained remote snapshot cannot look like a
// newly collected account count merely because the chart was regenerated.

// loadPoolCounts fills available counts after validating one complete pool export.
// The caller holds plugin.mu and supplies fresh catalog groups. Failure clears
// counts and records a display diagnostic; successful exports join by positive
// group ID and retain measured zero values separately from absent group records.
//
// Each request has a sixty-second budget matching PoolScope's collection budget.
// Transport redirects are rejected by the plugin client. Timestamp and duplicate
// validation completes before counts are assigned, preventing partial responses
// or ambiguous identities from mixing old and new observations.
func (plugin *Plugin) loadPoolCounts(ctx context.Context, state *ChannelState) {
	state.PoolAt, state.PoolError, state.PoolStale = "", "", false
	for index := range state.Groups {
		state.Groups[index].Available = nil
	}
	endpoint := strings.TrimSpace(plugin.env.Config.String("pool_stats_url"))
	if endpoint == "" {
		state.PoolError = "未配置透明号池统计地址"
		return
	}
	address, err := url.Parse(endpoint)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || !(address.Scheme == "https" || address.Scheme == "http" && (address.Hostname() == "localhost" || address.Hostname() == "127.0.0.1" || address.Hostname() == "::1")) {
		state.PoolError = "透明号池统计地址须使用 HTTPS（本机可用 HTTP）"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		state.PoolError = "无法创建透明号池统计请求"
		return
	}
	client := *plugin.client
	client.Timeout = 60 * time.Second
	response, err := client.Do(request)
	if err != nil {
		// Pool data is independent of channel observations. Continue with unknown
		// counts so an account-directory outage does not suppress the current channel chart.
		state.PoolError = "透明号池统计暂不可用"
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	var result struct {
		At     string `json:"snapshot_at"` // complete snapshot timestamp
		Stale  bool   `json:"stale"`       // retained snapshot warning
		Groups []struct {
			ID        int64  `json:"id"`                 // local group identifier
			Available *int64 `json:"available_accounts"` // measured active accounts
		} `json:"group_stats"`
	}
	if response.StatusCode != http.StatusOK {
		state.PoolError = poolResponseError(response.StatusCode, raw)
		return
	}
	if err != nil || len(raw) > 256<<10 || json.Unmarshal(raw, &result) != nil || result.Groups == nil {
		state.PoolError = "透明号池统计响应格式无效，请检查统计地址"
		return
	}
	// Allow a minute of clock skew between independently hosted source services.
	snapshot, err := time.Parse(time.RFC3339, result.At)
	if err != nil || snapshot.After(plugin.now().Add(time.Minute)) {
		state.PoolError = "透明号池统计缺少有效快照时间"
		return
	}
	counts := make(map[int64]*int64, len(result.Groups))
	for _, group := range result.Groups {
		if group.ID <= 0 || group.Available == nil || *group.Available < 0 {
			state.PoolError = "透明号池账号数量无效"
			return
		}
		if _, duplicate := counts[group.ID]; duplicate {
			state.PoolError = "透明号池统计包含重复分组"
			return
		}
		counts[group.ID] = group.Available
	}
	for index := range state.Groups {
		state.Groups[index].Available = counts[state.Groups[index].ID]
	}
	state.PoolAt = result.At
	// Pool collection normally refreshes once per minute. Older observations remain
	// visible with a stale label instead of being presented as newly collected counts.
	state.PoolStale = result.Stale || plugin.now().Sub(snapshot) > 5*time.Minute
}

// poolResponseError translates known export failures into actionable source diagnostics.
// Unknown upstream text stays out of the caption because the configured URL
// could return an unrelated page containing private or untrusted diagnostic content.
func poolResponseError(status int, raw []byte) string {
	var failure struct {
		Message string `json:"message"` // source error category
	}
	// Only a recognized error category changes the instruction. Malformed bodies retain
	// the status-based diagnostic, keeping arbitrary upstream text out of group messages.
	_ = json.Unmarshal(raw, &failure)
	if status == http.StatusNotFound && failure.Message == "公开号池统计未启用" {
		return "请在透明号池的插件原生配置中开启「公开分组的可用账号统计」并保存"
	}
	if status == http.StatusNotFound {
		return "透明号池统计接口不存在，请核对统计地址及插件版本"
	}
	if status == http.StatusServiceUnavailable {
		return "透明号池统计暂不可用，请检查号池同步状态及完整快照"
	}
	return "透明号池统计请求失败（HTTP " + strconv.Itoa(status) + "）"
}

// channelCaption appends available counts for the requested GPT pools after the
// channel observation summary. Other groups remain in the chart and do not
// receive account lines. Missing exported counts remain unknown, including a
// failed fetch whose previously collected quantities have been cleared.
func channelCaption(state ChannelState) string {
	var text strings.Builder
	text.WriteString("渠道运行状态 · " + time.Unix(state.Updated, 0).In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04 UTC+8") + "\n")
	text.WriteString("灰色表示暂无有效观测；首字和缓存取最近有效观测。\n")
	if state.PoolAt != "" {
		at, _ := time.Parse(time.RFC3339, state.PoolAt)
		text.WriteString("号池快照：" + at.In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04 UTC+8"))
		if state.PoolStale {
			text.WriteString("（旧快照）")
		}
	} else {
		text.WriteString(state.PoolError)
	}
	text.WriteString("\n")
	for _, label := range []string{"GPT满血", "GPT特惠"} {
		for _, group := range state.Groups {
			if poolSummaryLabel(group.Name) != label {
				continue
			}
			count := "暂无数据"
			if group.Available != nil {
				count = strconv.FormatInt(*group.Available, 10)
			}
			text.WriteString("\n[" + label + "] 可用账号：" + count)
		}
	}
	return text.String()
}

// poolSummaryLabel recognizes the two GPT pool labels requested for message text.
// Catalog names still identify the group in the picture; this shortening applies
// to caption labels after numeric IDs have matched the statistics response.
func poolSummaryLabel(name string) string {
	if !strings.Contains(strings.ToUpper(name), "GPT") {
		return ""
	}
	if strings.Contains(name, "满血") {
		return "GPT满血"
	}
	if strings.Contains(name, "特惠") {
		return "GPT特惠"
	}
	return ""
}

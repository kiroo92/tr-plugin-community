package app

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	pluginpb "github.com/TokenFlux/TokenRouter/plugin-sdk/go/pluginpb/v1"
)

// invites.go issues expiring website invitations and admits one requester per
// approval link. Eligibility comes from LinuxDO identities or completed balance
// orders. Invitation records belong to the configured bot and group, so each
// website account has an independent claim within that scope. Request links
// require approval even when the target group is private.
//
// Admission persists the consumed invitation before contacting Telegram. A
// definite API rejection releases that claim; an uncertain result keeps it
// consumed because Telegram may already have admitted the requester. Revocation
// follows approval and can be retried independently. Existing ordinary links
// are revoked before replacement because their member limit counts simultaneous
// members and cannot enforce a cumulative admission limit.

const lifetime = 10 * time.Minute

// Invite stores a website account's invitation and durable admission state.
type Invite struct {
	UserID  int64  `json:"user_id"`       // claiming website account
	URL     string `json:"url,omitempty"` // issued Telegram invitation
	Expires int64  `json:"expires"`       // expiration Unix seconds
	Created int64  `json:"created"`       // creation Unix seconds
	Used    bool   `json:"used"`          // admission claim consumed
	Revoke  bool   `json:"revoke"`        // revocation awaiting completion
	Revoked bool   `json:"revoked"`       // revocation considered complete
	Pending bool   `json:"pending"`       // creation outcome uncertain
	Source  string `json:"source"`        // website eligibility source

	// JoinRequest identifies links issued with approval enabled. False marks
	// legacy ordinary links, which issue revokes before creating a replacement.
	JoinRequest bool `json:"join_request"` // approval invitation mode
}

// qualification 记录满足入群资格的来源，并区分「已核验为否」与「无需核验」。
// Recharge 为 nil 表示已由 LinuxDO 绑定满足，未再扫描订单。
type qualification struct {
	Source   string `json:"source"`
	LinuxDO  bool   `json:"linuxdo"`
	Recharge *bool  `json:"recharge"`
}

func (plugin *Plugin) eligible(ctx context.Context, userID int64) (qualification, error) {
	var found qualification
	user, err := plugin.env.Host.Users.Get(ctx, userID)
	if err != nil {
		return found, errors.New("无法读取账号状态")
	}
	if user.Status != "active" {
		return found, nil
	}
	identities, err := plugin.env.Host.Users.Identities(ctx, userID)
	if err != nil {
		return found, errors.New("无法核验 LinuxDO 绑定")
	}
	for _, identity := range identities.Identities {
		if identity.GetProviderType() == "linuxdo" && identity.GetSubject() != "" {
			found.Source = "LinuxDO 绑定"
			found.LinuxDO = true
			return found, nil
		}
	}
	no := false
	found.Recharge = &no
	for page := int32(1); page <= 100; page++ {
		result, err := plugin.env.Host.Orders.ListForUser(ctx, &pluginpb.ListUserOrdersRequest{UserId: userID, Page: page, PageSize: 50, Status: "COMPLETED"})
		if err != nil {
			return qualification{}, errors.New("无法核验充值订单")
		}
		for _, order := range result.Orders {
			amount, valid := new(big.Rat).SetString(order.GetAmount())
			if order.GetType() == "balance" && order.GetStatus() == "COMPLETED" && order.GetPaidUnix() > 0 && valid && amount.Sign() > 0 {
				yes := true
				found.Source = "余额充值"
				found.Recharge = &yes
				return found, nil
			}
		}
		if int64(page)*50 >= result.Total {
			return found, nil
		}
		if len(result.Orders) == 0 {
			return qualification{}, errors.New("订单分页不完整，请稍后重试")
		}
	}
	// shortcut: 最多核验 5000 条订单，宿主提供资格存在性查询后替换分页扫描。
	return qualification{}, errors.New("历史订单过多，请联系管理员核实")
}

func (plugin *Plugin) inviteKey(userID int64) string {
	return plugin.scoped("invite/" + strconv.FormatInt(userID, 10))
}

// issue creates or reuses an approval invitation for the eligible website user.
// Callers hold plugin.mu and supply the verified eligibility source. Consumed
// claims and unresolved creations return errors to prevent another admission.
//
// Ordinary links from previous versions are revoked before replacement. The
// pending record is saved before creation so a timeout cannot immediately issue
// another link, and revocation is attempted if saving the returned link fails.
func (plugin *Plugin) issue(ctx context.Context, userID int64, source string) (Invite, error) {
	var current Invite
	if _, err := plugin.load(ctx, plugin.inviteKey(userID), &current); err != nil {
		return current, err
	}
	if current.Used {
		return current, errors.New("该账号已使用入群资格")
	}
	if current.Revoke {
		return current, errors.New("旧邀请正在撤销，请稍后重试")
	}
	if current.Expires > plugin.now().Unix() {
		if current.Pending {
			return current, errors.New("上次创建结果待确认，请在 10 分钟有效期结束后重试")
		}
		if !current.Revoked && current.JoinRequest {
			return current, nil
		}
		if !current.Revoked {
			// Ordinary public-group links can lose their attribution in clients.
			// Revoke them before publishing an invitation that requires link approval.
			if err := plugin.revoke(ctx, &current); err != nil {
				return current, err
			}
		}
	}
	if err := plugin.checkBot(ctx); err != nil {
		return current, err
	}
	current = Invite{
		UserID:      userID,
		Source:      source,
		Created:     plugin.now().Unix(),
		Expires:     plugin.now().Add(lifetime).Unix(),
		Pending:     true,
		JoinRequest: true,
	}
	// Persist creation intent before the request: a lost response must keep
	// subsequent claims blocked until this invitation's validity window expires.
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		return current, err
	}
	var result struct {
		Link string `json:"invite_link"`
	}
	// Telegram forbids combining creates_join_request with member_limit. Admission
	// uses the persisted claim so queued requesters cannot consume the link twice.
	err := plugin.telegram(ctx, "createChatInviteLink", map[string]any{
		"chat_id":              plugin.chat,
		"expire_date":          current.Expires,
		"creates_join_request": true,
		"name":                 "web-" + strconv.FormatInt(userID, 10),
	}, &result)
	if err != nil {
		return Invite{}, errors.New("创建邀请未确认，避免重复生成，请在 10 分钟后重试")
	}
	parsed, err := url.Parse(result.Link)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "t.me" || parsed.User != nil || !strings.HasPrefix(parsed.Path, "/+") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Invite{}, errors.New("Telegram 返回了无效邀请")
	}
	current.URL = result.Link
	current.Pending = false
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		// The pending record remains authoritative when saving the URL fails.
		// Attempt revocation so the untracked link cannot accumulate usable requests.
		_ = plugin.telegram(ctx, "revokeChatInviteLink", map[string]any{"chat_id": plugin.chat, "invite_link": current.URL}, nil)
		return Invite{}, err
	}
	// Invitation persistence determines claim success. Event storage is diagnostic,
	// so failure to append an event does not discard the issued invitation.
	_ = plugin.event(ctx, "生成入群邀请", "网站账号 "+strconv.FormatInt(userID, 10)+"，有效期 10 分钟。")
	return current, nil
}

func (plugin *Plugin) revoke(ctx context.Context, current *Invite) error {
	if current.URL == "" {
		return errors.New("邀请创建结果待确认，无法手动撤销")
	}
	current.Revoke = true
	if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
		return err
	}
	if err := plugin.telegram(ctx, "revokeChatInviteLink", map[string]any{"chat_id": plugin.chat, "invite_link": current.URL}, nil); err != nil {
		if current.Expires > plugin.now().Unix() {
			return err
		}
	}
	current.Revoke = false
	current.Revoked = true
	return plugin.save(ctx, plugin.inviteKey(current.UserID), current)
}

// inviteByLink 用链接反查领取记录；不存在时返回 ok=false。
func (plugin *Plugin) inviteByLink(ctx context.Context, link string) (Invite, bool, error) {
	var raw string
	err := plugin.db.QueryRowContext(ctx, "SELECT value FROM records WHERE key LIKE ? AND json_extract(value,'$.url')=? LIMIT 1", plugin.scoped("invite/%"), link).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, false, nil
	}
	if err != nil {
		return Invite{}, false, errors.New("匹配入群邀请失败")
	}
	var current Invite
	if err := decodeRecord(raw, &current); err != nil {
		return Invite{}, false, err
	}
	return current, true, nil
}

// resolveJoin admits the first requester using a live approval invitation.
// Callers hold plugin.mu across lookup, persistence, and Telegram requests, so
// later requests observe the consumed claim even if link revocation fails.
// Missing attribution, legacy links, and unavailable invitations are declined.
//
// The claim is saved before approval and released after a definite API
// rejection. Transport failures and uncertain server responses retain the claim
// because approval may have succeeded; administrators must verify those outcomes.
func (plugin *Plugin) resolveJoin(ctx context.Context, userID int64, link string) error {
	current, found, err := plugin.inviteByLink(ctx, link)
	if err != nil {
		return err
	}
	if !found {
		if err := plugin.telegram(ctx, "declineChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
			return err
		}
		detail := "请求中的邀请链接未匹配到网站领取记录，已自动拒绝。"
		if link == "" {
			detail = "Telegram 入群请求未携带邀请链接，无法核对领取记录；请使用网站新领取的审批邀请。"
		}
		return plugin.event(ctx, "拒绝了入群请求", detail)
	}
	if !current.JoinRequest || current.Used || current.Revoked ||
		current.Revoke || current.Expires <= plugin.now().Unix() {
		if err := plugin.telegram(ctx, "declineChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
			return err
		}
		detail := "网站账号 " + strconv.FormatInt(current.UserID, 10) + " 的邀请已使用、已过期或正在撤销。"
		if !current.JoinRequest {
			detail = "此邀请使用旧版链接模式，请回网站重新领取审批邀请。"
		}
		return plugin.event(ctx, "拒绝了不可用的邀请", detail)
	}
	// Save consumption before approval: a crash after Telegram admits this user
	// must leave the link unavailable to every later requester after restart.
	current.Used = true
	current.Revoke = true
	if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
		return err
	}
	if err := plugin.telegram(ctx, "approveChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
		var rejection *telegramRejection
		if !errors.As(err, &rejection) {
			// Telegram may have committed approval before the response was lost.
			// Retain consumption and pending revocation so another requester cannot enter.
			return errors.New("入群批准结果未确认，邀请已锁定；请管理员核实成员状态")
		}
		// A validated client-error response confirms this approval was rejected.
		// Release the claim so a later request can retry the unused invitation.
		current.Used = false
		current.Revoke = false
		if saveErr := plugin.save(ctx, plugin.inviteKey(current.UserID), current); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err := plugin.revoke(ctx, &current); err != nil {
		// Admission is already consumed. The durable revocation flag lets polling
		// retry cleanup while subsequent requests continue to be declined locally.
		return plugin.event(ctx, "批准了入群请求", "网站账号 "+strconv.FormatInt(current.UserID, 10)+" 已获批准，邀请已锁定，撤销将在后台重试。")
	}
	return plugin.event(ctx, "批准了入群请求", "网站账号 "+strconv.FormatInt(current.UserID, 10)+" 通过领取的邀请入群，链接已撤销。")
}

func (plugin *Plugin) invites(ctx context.Context, before int64) ([]Invite, error) {
	rows, err := plugin.db.QueryContext(ctx, "SELECT value FROM records WHERE key LIKE ? AND CAST(json_extract(value,'$.user_id') AS INTEGER)>? ORDER BY CAST(json_extract(value,'$.user_id') AS INTEGER) LIMIT 51", plugin.scoped("invite/%"), before)
	if err != nil {
		return nil, errors.New("无法读取邀请记录")
	}
	defer rows.Close()
	records := make([]Invite, 0)
	for rows.Next() {
		var raw string
		var record Invite
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := decodeRecord(raw, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

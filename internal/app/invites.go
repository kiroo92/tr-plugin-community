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

const lifetime = 10 * time.Minute

type Invite struct {
	UserID  int64  `json:"user_id"`
	URL     string `json:"url,omitempty"`
	Expires int64  `json:"expires"`
	Created int64  `json:"created"`
	Used    bool   `json:"used"`
	Revoke  bool   `json:"revoke"`
	Revoked bool   `json:"revoked"`
	Pending bool   `json:"pending"`
	Source  string `json:"source"`
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
		if !current.Revoked {
			return current, nil
		}
	}
	if err := plugin.checkBot(ctx); err != nil {
		return current, err
	}
	current = Invite{UserID: userID, Source: source, Created: plugin.now().Unix(), Expires: plugin.now().Add(lifetime).Unix(), Pending: true}
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		return current, err
	}
	var result struct {
		Link string `json:"invite_link"`
	}
	err := plugin.telegram(ctx, "createChatInviteLink", map[string]any{"chat_id": plugin.chat, "expire_date": current.Expires, "member_limit": 1, "name": "web-" + strconv.FormatInt(userID, 10)}, &result)
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
		_ = plugin.telegram(ctx, "revokeChatInviteLink", map[string]any{"chat_id": plugin.chat, "invite_link": current.URL}, nil)
		return Invite{}, err
	}
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

// rejectJoin 拒绝通过插件链接发起的入群请求并记录事件。
// 群级审批只拦直接加入，链接本身不核对 Telegram 身份，转发后无法归属到领取者，
// 因此对这类请求一律拒绝，要求本人从网站重新领取。
func (plugin *Plugin) rejectJoin(ctx context.Context, userID int64, link string) error {
	current, found, err := plugin.inviteByLink(ctx, link)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := plugin.telegram(ctx, "declineChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
		return err
	}
	return plugin.event(ctx, "拒绝了入群请求", "链接对应网站账号 "+strconv.FormatInt(current.UserID, 10)+"；链接可能已被转发，请本人重新领取。")
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

package app

import (
	"context"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	pluginpb "github.com/TokenFlux/TokenRouter/plugin-sdk/go/pluginpb/v1"
)

// invites.go checks website eligibility and admits each bound Telegram account
// using its durable website claim. Eligibility comes from LinuxDO identities or
// completed balance orders. Invitation records belong to the configured bot and
// group, so each
// website account has an independent claim within that scope. Permanent bot
// entry tokens establish ownership before an admission request is considered.
//
// Admission persists the consumed invitation before contacting Telegram. A
// definite API rejection releases that claim; an uncertain result keeps it
// consumed because Telegram may already have admitted the requester. Revocation
// follows approval and can be retried independently. Existing ordinary links
// are revoked before replacement because their member limit counts simultaneous
// members and cannot enforce a cumulative admission limit. Group invitations
// have no expiry; incoming requests identify their owners through Telegram
// bindings even when link attribution is absent.

// invitationRetryDelay spaces uncertain creation attempts so repeated Start
// updates do not immediately create another permanent group link.
const invitationRetryDelay = 30 * time.Second

// Invite stores a website account's invitation and durable admission state.
type Invite struct {
	UserID int64  `json:"user_id"`       // claiming website account
	URL    string `json:"url,omitempty"` // issued Telegram invitation
	// Expires identifies legacy invitations that need replacement. Zero means the
	// group invitation has no expiry and can be reused until explicitly revoked.
	Expires int64  `json:"expires"` // legacy expiry Unix seconds
	Created int64  `json:"created"` // creation Unix seconds
	Used    bool   `json:"used"`    // admission claim consumed
	Revoke  bool   `json:"revoke"`  // revocation awaiting completion
	Revoked bool   `json:"revoked"` // revocation considered complete
	Pending bool   `json:"pending"` // creation outcome uncertain
	Source  string `json:"source"`  // website eligibility source

	// RetryAt delays another attempt when group invitation creation is uncertain.
	// It controls API retries independently of either link's validity or lifetime.
	RetryAt int64 `json:"retry_at,omitempty"` // creation retry Unix seconds

	// JoinRequest identifies links issued with approval enabled. False marks
	// legacy ordinary links, which issue revokes before creating a replacement.
	JoinRequest bool `json:"join_request"` // approval invitation mode

	// StartToken owns the permanent bot entry. TelegramID and BoundAt are set
	// together by its first private Start sender and survive group invitation renewal.
	StartToken string `json:"start_token,omitempty"` // permanent random entry token
	TelegramID int64  `json:"telegram_id"`           // bound Telegram user identifier
	BoundAt    int64  `json:"bound_at"`              // binding time Unix seconds
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

// issue creates or reuses a group invitation for a bound website user.
// Callers hold plugin.mu and supply the verified eligibility source. Consumed
// claims and unresolved creations return errors to prevent another admission.
//
// Expiring and ordinary links from previous versions are revoked before
// replacement. Creation intent is saved with a retry delay before contacting
// Telegram, so an uncertain response cannot cause an immediate second creation.
// Successful links remain reusable across time and process restarts until used
// or revoked, and saving the returned link is followed by private delivery.
func (plugin *Plugin) issue(ctx context.Context, userID int64, source string) (Invite, error) {
	var current Invite
	if _, err := plugin.load(ctx, plugin.inviteKey(userID), &current); err != nil {
		return current, err
	}
	if current.TelegramID <= 0 || current.StartToken == "" {
		return current, errors.New("请先通过专属机器人链接完成 Telegram 绑定")
	}
	if current.Used {
		return current, errors.New("该账号已使用入群资格")
	}
	if current.Revoke {
		return current, errors.New("旧邀请正在撤销，请稍后重试")
	}
	if current.Pending && current.RetryAt > plugin.now().Unix() {
		return current, errors.New("上次创建邀请结果待确认，请稍后重试")
	}
	if current.URL != "" && !current.Pending && !current.Revoked && current.JoinRequest && current.Expires == 0 {
		return current, nil
	}
	if current.URL != "" && !current.Revoked {
		// Replace legacy invitations before delivering access: an existing expiry
		// or ordinary link mode must not survive through the permanent-link flow.
		if err := plugin.revoke(ctx, &current); err != nil {
			return current, err
		}
	}
	if err := plugin.checkBot(ctx); err != nil {
		return current, err
	}
	// Replace the group entry on its existing record. Its permanent Start token
	// and bound Telegram identity survive revocation and creation of another invitation.
	current.UserID = userID
	current.Source = source
	current.Expires = 0
	current.RetryAt = plugin.now().Add(invitationRetryDelay).Unix()
	current.Pending = true
	current.JoinRequest = true
	current.URL = ""
	current.Revoked = false
	// Persist retry timing before the request: a lost response delays another
	// creation attempt while ownership continues to restrict any untracked invitations.
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		return current, err
	}
	var result struct {
		Link     string `json:"invite_link"`
		Approval bool   `json:"creates_join_request"` // Telegram-confirmed approval mode
		Expires  int64  `json:"expire_date"`          // Telegram-reported expiry timestamp
	}
	// Telegram forbids combining creates_join_request with member_limit. Admission
	// uses the persisted claim so queued requesters cannot consume the link twice.
	err := plugin.telegram(ctx, "createChatInviteLink", map[string]any{
		"chat_id":              plugin.chat,
		"creates_join_request": true,
		"name":                 "web-" + strconv.FormatInt(userID, 10),
	}, &result)
	if err != nil {
		return Invite{}, errors.New("创建邀请结果未确认，请稍后重试")
	}
	parsed, err := url.Parse(result.Link)
	if err != nil || !result.Approval || result.Expires != 0 || parsed.Scheme != "https" || parsed.Host != "t.me" || parsed.User != nil || !strings.HasPrefix(parsed.Path, "/+") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Invite{}, errors.New("Telegram 返回了无效邀请")
	}
	current.URL = result.Link
	current.Pending = false
	current.RetryAt = 0
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		// The pending record remains authoritative when saving the URL fails.
		// Attempt revocation so the untracked link cannot accumulate usable requests.
		_ = plugin.telegram(ctx, "revokeChatInviteLink", map[string]any{"chat_id": plugin.chat, "invite_link": current.URL}, nil)
		return Invite{}, err
	}
	// Invitation persistence determines claim success. Event storage is diagnostic,
	// so failure to append an event does not discard the issued invitation.
	_ = plugin.event(ctx, "生成入群邀请", bindingDetail(current)+"，专属群邀请已生成。")
	return current, nil
}

// revoke invalidates a known group invitation and persists cleanup progress.
// Callers hold plugin.mu; failed revocation remains pending for polling retries.
// Permanent invitations require Telegram to confirm revocation before local completion.
//
// An already expired legacy invitation can complete cleanup after an API error
// because its stored expiry prevents future joins. Zero expiry identifies a
// permanent invitation, so failed cleanup of those records remains retryable.
func (plugin *Plugin) revoke(ctx context.Context, current *Invite) error {
	if current.URL == "" {
		return errors.New("邀请创建结果待确认，无法手动撤销")
	}
	// Persist cleanup intent before contacting Telegram so a crash or network
	// error leaves a durable retry for invitations that remain valid indefinitely.
	current.Revoke = true
	if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
		return err
	}
	if err := plugin.telegram(ctx, "revokeChatInviteLink", map[string]any{"chat_id": plugin.chat, "invite_link": current.URL}, nil); err != nil {
		if current.Expires == 0 || current.Expires > plugin.now().Unix() {
			return err
		}
		// A legacy invitation with a positive past expiry cannot accept joins.
		// Complete its local cleanup while permanent invitations continue retrying errors above.
	}
	current.Revoke = false
	current.Revoked = true
	return plugin.save(ctx, plugin.inviteKey(current.UserID), current)
}

// resolveJoin admits the Telegram owner recorded by a permanent bot entry.
// Callers hold plugin.mu across lookup, persistence, and Telegram requests, so
// later requests observe the consumed claim even if link revocation fails.
// Group invitation attribution is unnecessary because the requester identifies
// the owning website account through its earlier private Start binding.
//
// The claim is saved before approval and released after a definite API
// rejection. Transport failures and uncertain server responses retain the claim
// because approval may have succeeded; administrators must verify those outcomes.
func (plugin *Plugin) resolveJoin(ctx context.Context, userID int64) error {
	current, found, err := plugin.inviteForTelegram(ctx, userID)
	if err != nil {
		return err
	}
	if !found || current.StartToken == "" {
		if err := plugin.telegram(ctx, "declineChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
			return err
		}
		return plugin.event(ctx, "拒绝了入群请求", "申请账号尚未完成绑定，请先打开网站专属机器人链接并点击 Start。")
	}
	qualification, err := plugin.eligible(ctx, current.UserID)
	if err != nil {
		return err
	}
	var group GroupState
	if _, err := plugin.load(ctx, plugin.scoped("group"), &group); err != nil {
		return err
	}
	if current.Used || qualification.Source == "" || group.Public && !group.Approval {
		if err := plugin.telegram(ctx, "declineChatJoinRequest", map[string]any{"chat_id": plugin.chat, "user_id": userID}, nil); err != nil {
			return err
		}
		return plugin.event(ctx, "拒绝了不可用的入群资格", bindingDetail(current)+" 的资格已使用或暂不可用，请重新打开机器人核实。")
	}
	// Save consumption before approval: a crash after Telegram admits this user
	// must leave the link unavailable to every later requester after restart.
	current.Used = true
	cleanup := current.URL != "" && !current.Revoked
	current.Revoke = cleanup
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
	if cleanup {
		if err := plugin.revoke(ctx, &current); err != nil {
			// Admission is already consumed. The durable revocation flag lets polling
			// retry cleanup while subsequent requests continue to be declined locally.
			return plugin.event(ctx, "批准了入群请求", bindingDetail(current)+" 已获批准，群邀请撤销将在后台重试。")
		}
	}
	return plugin.event(ctx, "批准了入群请求", bindingDetail(current)+" 已通过账号绑定核验。")
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

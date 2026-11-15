package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// bot_links.go issues permanent bot entry links and binds their first private
// Start sender to the owning website account. Each invitation record contains
// one random entry token and one Telegram identity within the configured bot
// and group scope. Website responses reconstruct the bot URL from that token;
// group invitations are delivered through the resulting private conversation.
//
// Binding is persisted before any group entry is delivered, so a lost message
// can be retried by the same sender after restart. The shared plugin mutex
// serializes token redemption and reverse identity checks. A Telegram account
// already assigned to another website account cannot redeem another token, and
// a redeemed token cannot replace its recorded Telegram owner.

// startTokenBytes determines the random payload size shared by token generation and redemption validation.
const startTokenBytes = 32

// issueBotLink creates or reuses a permanent entry token for an eligible user.
// Callers hold plugin.mu and supply a freshly verified eligibility source.
// Existing bindings and admission outcomes survive repeated website claims.
//
// Connection checks establish the bot username used by the returned URL. Public
// groups require group approval because their public entry otherwise admits
// accounts before the bot can inspect their binding. Random token persistence
// precedes publication, so an interrupted response returns the same link later.
func (plugin *Plugin) issueBotLink(ctx context.Context, userID int64, source string) (Invite, error) {
	if err := plugin.checkBot(ctx); err != nil {
		return Invite{}, err
	}
	var group GroupState
	if _, err := plugin.load(ctx, plugin.scoped("group"), &group); err != nil {
		return Invite{}, err
	}
	if group.Public && !group.Approval {
		return Invite{}, errors.New("公开群须先开启群级入群审批")
	}
	var current Invite
	if _, err := plugin.load(ctx, plugin.inviteKey(userID), &current); err != nil {
		return Invite{}, err
	}
	if current.StartToken == "" {
		// Persist one cryptographically random token per account. Its lifetime is
		// independent of the temporary group invitation and survives process restarts.
		random := make([]byte, startTokenBytes)
		if _, err := rand.Read(random); err != nil {
			return Invite{}, errors.New("无法生成机器人专属链接")
		}
		current.StartToken = base64.RawURLEncoding.EncodeToString(random)
		current.UserID = userID
		if current.Created == 0 {
			current.Created = plugin.now().Unix()
		}
	}
	current.Source = source
	// Commit the token before HTTP serialization so a disconnected website client
	// receives its existing entry when it retries rather than losing the first link.
	if err := plugin.save(ctx, plugin.inviteKey(userID), current); err != nil {
		return Invite{}, err
	}
	return current, nil
}

// websiteInvite returns entry-link metadata for the authenticated website owner.
// Its URL opens the bot and has no time expiry. Stored group URLs and entry
// token fields are replaced before the response is serialized.
func (plugin *Plugin) websiteInvite(ctx context.Context, current Invite, visible bool) (Invite, error) {
	var bot Bot
	if _, err := plugin.load(ctx, plugin.scoped("bot"), &bot); err != nil {
		return Invite{}, err
	}
	current.URL = ""
	if visible && current.StartToken != "" && bot.Username != "" {
		current.URL = "https://t.me/" + url.PathEscape(bot.Username) + "?start=" + current.StartToken
	}
	current.StartToken = ""
	// Website expiry describes the permanent bot entry. The stored group expiry
	// belongs to private invitation delivery and must not become a website countdown.
	current.Expires = 0
	return current, nil
}

// inviteForTelegram finds the website invitation owned by a bound Telegram user.
// A nonpositive identifier has no owner. Callers hold plugin.mu when combining
// this lookup with binding mutations or admission decisions.
func (plugin *Plugin) inviteForTelegram(ctx context.Context, telegramID int64) (Invite, bool, error) {
	if telegramID <= 0 {
		return Invite{}, false, nil
	}
	var raw string
	err := plugin.db.QueryRowContext(ctx,
		"SELECT value FROM records WHERE key LIKE ? AND json_extract(value,'$.telegram_id')=? LIMIT 1",
		plugin.scoped("invite/%"), telegramID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		// An absent owner is an expected admission outcome. Return no match so
		// the caller declines unbound requesters without exposing another website account.
		return Invite{}, false, nil
	}
	if err != nil {
		return Invite{}, false, errors.New("无法读取 Telegram 绑定")
	}
	var current Invite
	if err := decodeRecord(raw, &current); err != nil {
		return Invite{}, false, err
	}
	return current, true, nil
}

// inviteForStart finds an account by a permanent bot entry token.
// Payloads must decode to the issued random-token size before storage is read.
// Empty or malformed payloads return no match and reveal no account metadata.
func (plugin *Plugin) inviteForStart(ctx context.Context, token string) (Invite, bool, error) {
	// Entry payloads encode exactly thirty-two random bytes.
	rawToken, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(rawToken) != startTokenBytes {
		return Invite{}, false, nil
	}
	var raw string
	err = plugin.db.QueryRowContext(ctx,
		"SELECT value FROM records WHERE key LIKE ? AND json_extract(value,'$.start_token')=? LIMIT 1",
		plugin.scoped("invite/%"), token,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown or revoked payloads are ordinary invalid entries. Return no match
		// so the private response can request a fresh authenticated website claim.
		return Invite{}, false, nil
	}
	if err != nil {
		return Invite{}, false, errors.New("无法读取机器人专属链接")
	}
	var current Invite
	if err := decodeRecord(raw, &current); err != nil {
		return Invite{}, false, err
	}
	return current, true, nil
}

// handleStart binds a private Start sender and delivers that account's group entry.
// The caller holds plugin.mu and supplies a private message whose sender and
// chat identifiers agree. Bare Start resumes a previously bound account.
//
// Eligibility is checked again before binding or delivery because permanent
// entry links can outlive the website account's qualification. Reverse identity
// checks and persistence complete before any response containing a group entry
// is sent. Delivery failures therefore retain an established binding for retry.
func (plugin *Plugin) handleStart(ctx context.Context, message *Message) error {
	if message.From.ID <= 0 || message.Chat.Type != "private" || message.Chat.ID != message.From.ID || message.From.Bot {
		return nil
	}
	parts := strings.Fields(message.Text)
	if len(parts) == 0 || parts[0] != "/start" {
		return nil
	}
	if !plugin.settings.Invites {
		return plugin.botReply(ctx, message.From.ID, "管理员已暂停社群入口，请稍后再试。", "")
	}
	var current Invite
	var found bool
	var err error
	if len(parts) == 2 {
		current, found, err = plugin.inviteForStart(ctx, parts[1])
	} else if len(parts) == 1 {
		// Bare Start resumes the sender's existing ownership. This lets a bound
		// user renew a temporary group invitation through the bot's normal Start button.
		current, found, err = plugin.inviteForTelegram(ctx, message.From.ID)
	}
	if err != nil {
		return err
	}
	if !found || current.StartToken == "" {
		return plugin.botReply(ctx, message.From.ID, "请先登录网站社群中心，打开你的专属机器人链接并点击 Start。", "")
	}
	if current.TelegramID != 0 && current.TelegramID != message.From.ID {
		return plugin.botReply(ctx, message.From.ID, "此专属链接已绑定其他 Telegram 账号，请使用本人网站账号领取链接。", "")
	}
	other, linked, err := plugin.inviteForTelegram(ctx, message.From.ID)
	if err != nil {
		return err
	}
	if linked && other.UserID != current.UserID {
		return plugin.botReply(ctx, message.From.ID, "此 Telegram 账号已绑定另一个网站账号。", "")
	}
	qualification, err := plugin.eligible(ctx, current.UserID)
	if err != nil {
		return err
	}
	if qualification.Source == "" {
		return plugin.botReply(ctx, message.From.ID, "网站账号暂不满足社群资格，请先绑定 LinuxDO 或完成余额充值。", "")
	}
	if current.TelegramID == 0 {
		// Persist ownership before delivering access. A lost response must allow
		// this sender to retry while preventing another sender from taking the token.
		current.TelegramID = message.From.ID
		current.BoundAt = plugin.now().Unix()
		current.Source = qualification.Source
		if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
			return err
		}
	}
	if current.Used {
		return plugin.botReply(ctx, message.From.ID, "该网站账号已使用入群资格。如入群结果未确认，请联系管理员核实。", "")
	}
	if err := plugin.checkBot(ctx); err != nil {
		return err
	}
	var group GroupState
	if _, err := plugin.load(ctx, plugin.scoped("group"), &group); err != nil {
		return err
	}
	if group.Public && !group.Approval {
		return plugin.botReply(ctx, message.From.ID, "绑定已完成，请联系管理员开启群级入群审批。", "")
	}
	current, err = plugin.issue(ctx, current.UserID, qualification.Source)
	if err != nil {
		return err
	}
	return plugin.botReply(ctx, message.From.ID, "绑定已完成。点击下方邀请链接申请加入群组，机器人会按你的绑定账号自动审批。\n"+current.URL, current.URL)
}

// botReply sends private onboarding text with an optional group-entry button.
// The recipient is a user who started this bot. Telegram delivery errors are
// returned so polling can report them and the sender can retry Start.
func (plugin *Plugin) botReply(ctx context.Context, telegramID int64, text, entry string) error {
	input := map[string]any{"chat_id": telegramID, "text": text}
	if entry != "" {
		input["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]string{{{"text": "加入交流群", "url": entry}}},
		}
	}
	return plugin.telegram(ctx, "sendMessage", input, nil)
}

// bindingDetail identifies the website and Telegram owners in administrator events.
func bindingDetail(current Invite) string {
	return "网站账号 " + strconv.FormatInt(current.UserID, 10) + "，Telegram " + strconv.FormatInt(current.TelegramID, 10)
}

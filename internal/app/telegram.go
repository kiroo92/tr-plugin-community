package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// telegram.go exchanges Bot API messages and routes group updates to invitation,
// assistant, and channel handlers. The configured bot needs invitation rights,
// and public groups need group approval to restrict their public entry. Updates
// contain requester identities, which admission checks against bindings created
// by private Start messages. The polling loop serializes these operations with HTTP calls.
//
// Response parsing distinguishes a validated client rejection from an uncertain
// transport or server outcome. Admission uses that distinction to decide whether
// a consumed claim may be released. Polling retries persisted revocations before
// handling further updates, while offsets retain progress across process restarts
// and prevent replay of assistant requests whose external effects are uncertain.

// telegramRejection identifies a validated Bot API refusal of a request.
type telegramRejection struct {
	method string // rejected API method
	code   int    // Telegram HTTP status
}

// Error returns a diagnostic that excludes request credentials and payloads.
func (err *telegramRejection) Error() string {
	return fmt.Sprintf("Telegram %s 失败（%d），检查机器人权限或稍后重试", err.method, err.code)
}

func decodeRecord(raw string, target any) error {
	if json.Unmarshal([]byte(raw), target) != nil {
		return errors.New("插件数据损坏")
	}
	return nil
}
func (plugin *Plugin) telegram(ctx context.Context, method string, input any, target any) error {
	var retry int64
	if _, err := plugin.load(ctx, plugin.scoped("retry"), &retry); err != nil {
		return err
	}
	if retry > plugin.now().Unix() {
		return errors.New("Telegram 请求冷却中，请稍后重试")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, plugin.telegramURL+"/bot"+plugin.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("无法创建 Telegram 请求")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := plugin.client.Do(request)
	if err != nil {
		return errors.New("Telegram 网络请求失败")
	}
	defer response.Body.Close()
	return plugin.telegramResult(ctx, method, response, target)
}

// telegramResult decodes a bounded Bot API response and records requested cooldowns.
// The caller owns response.Body and closes it after parsing. Successful results
// populate target when supplied, while malformed or oversized bodies return errors.
//
// A matching client status and Telegram error code produce telegramRejection,
// allowing admission to release a rejected claim. Server errors, contradictory
// responses, and incomplete bodies leave the outcome uncertain because callers
// cannot infer whether Telegram performed the requested operation.
func (plugin *Plugin) telegramResult(ctx context.Context, method string, response *http.Response, target any) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(raw) > 2<<20 {
		return errors.New("Telegram 响应无效或过大")
	}
	var result struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			Retry int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return errors.New("Telegram 响应格式无效")
	}
	if !result.OK || response.StatusCode != 200 {
		if result.Parameters.Retry > 0 {
			// Persist Telegram's retry interval so HTTP and background callers share
			// the same cooldown across process restarts and avoid repeated rate-limit requests.
			if err := plugin.save(ctx, plugin.scoped("retry"), plugin.now().Unix()+result.Parameters.Retry); err != nil {
				return err
			}
		}
		if !result.OK && result.Code == response.StatusCode && result.Code >= 400 && result.Code < 500 {
			return &telegramRejection{method: method, code: result.Code}
		}
		return fmt.Errorf("Telegram %s 失败（%d），检查机器人权限或稍后重试", method, response.StatusCode)
	}
	if target != nil && json.Unmarshal(result.Result, target) != nil {
		return errors.New("Telegram 结果格式无效")
	}
	return nil
}

type Bot struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// GroupState 记录检查连接时读到的目标群组属性，用于管理端提示门禁是否成立。
type GroupState struct {
	ChatID   string `json:"chat_id"`
	Public   bool   `json:"public"`
	Username string `json:"username,omitempty"`
	Approval bool   `json:"approval"`
}

func (plugin *Plugin) checkBot(ctx context.Context) error {
	if plugin.token == "" {
		return errors.New("请先在宿主插件配置中填写 Bot Token 和群组 ID")
	}
	var bot Bot
	if err := plugin.telegram(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return err
	}
	var hook struct {
		URL string `json:"url"`
	}
	if err := plugin.telegram(ctx, "getWebhookInfo", map[string]any{}, &hook); err != nil {
		return err
	}
	if hook.URL != "" {
		return errors.New("机器人已有 Webhook，请使用专用机器人；插件不会删除已有 Webhook")
	}
	var chat struct {
		Type          string `json:"type"`
		Username      string `json:"username"`
		JoinByRequest bool   `json:"join_by_request"`
		JoinToSend    bool   `json:"join_to_send_messages"`
	}
	if err := plugin.telegram(ctx, "getChat", map[string]any{"chat_id": plugin.chat}, &chat); err != nil {
		return err
	}
	if chat.Type != "supergroup" {
		return errors.New("需要超级群组（公开或私有均可）")
	}
	// 公开群必须开启群级审批才能形成门禁：否则任何人直接用 @用户名 就能进群。
	// 开启审批后由插件按 chat_join_request 携带的链接决定批准或拒绝。
	group := GroupState{ChatID: plugin.chat, Public: chat.Username != "", Username: chat.Username, Approval: chat.JoinByRequest}
	if err := plugin.save(ctx, plugin.scoped("group"), group); err != nil {
		return err
	}
	var member struct {
		Status string `json:"status"`
		Invite bool   `json:"can_invite_users"`
	}
	if err := plugin.telegram(ctx, "getChatMember", map[string]any{"chat_id": plugin.chat, "user_id": bot.ID}, &member); err != nil {
		return err
	}
	if member.Status != "administrator" || !member.Invite {
		return errors.New("机器人需要管理员和邀请用户权限")
	}
	if err := plugin.save(ctx, plugin.scoped("bot"), bot); err != nil {
		return err
	}
	plugin.checkedAt = plugin.now().Unix()
	return nil
}

type TGUser struct {
	ID  int64 `json:"id"`
	Bot bool  `json:"is_bot"`
}

// Message carries Telegram text, sender identity, and chat routing information.
type Message struct {
	ID   int64  `json:"message_id"`
	Date int64  `json:"date"`
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"` // Telegram chat category
	} `json:"chat"`
	From  TGUser   `json:"from"`
	Reply *Message `json:"reply_to_message"`
}

// Update carries Telegram messages, membership changes, and optional join attribution.
type Update struct {
	ID      int64    `json:"update_id"`
	Message *Message `json:"message"`
	Member  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Invite struct {
			Link string `json:"invite_link"`
		} `json:"invite_link"`
		New struct {
			Status   string `json:"status"`
			IsMember bool   `json:"is_member"`
			User     TGUser `json:"user"` // resulting membership owner
		} `json:"new_chat_member"`
	} `json:"chat_member"`
	Join *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		// Invite contains Telegram's optional invitation attribution. Link stays empty
		// when the join request arrives through a public entry without an invite.
		Invite struct {
			Link string `json:"invite_link"`
		} `json:"invite_link"`
	} `json:"chat_join_request"`
}

// poll processes one batch of Telegram updates for the configured group.
// The caller holds plugin.mu so admission, HTTP claims, and offset writes run
// in one sequence. Bot permissions are refreshed periodically and persisted
// revocations are retried before accepting another batch.
//
// Private Start messages establish bindings before entry delivery, and membership
// events consume the corresponding owner's claim. Join requests delegate approval
// to resolveJoin. Assistant messages advance the offset before external effects;
// other updates advance after handling so interrupted work can be reconsidered.
func (plugin *Plugin) poll(ctx context.Context) error {
	var bot Bot
	found, err := plugin.load(ctx, plugin.scoped("bot"), &bot)
	if err != nil {
		return err
	}
	if !found || plugin.checkedAt == 0 || plugin.now().Unix()-plugin.checkedAt >= 300 {
		if err := plugin.checkBot(ctx); err != nil {
			// A failed refresh invalidates the remembered permission check. The next
			// poll retries verification before admitting users or handling assistant messages.
			plugin.checkedAt = 0
			return err
		}
	}
	if err := plugin.retryRevokes(ctx); err != nil {
		return err
	}
	var offset int64
	if _, err := plugin.load(ctx, plugin.scoped("offset"), &offset); err != nil {
		return err
	}
	var updates []Update
	if err := plugin.telegram(ctx, "getUpdates", map[string]any{"offset": offset, "limit": 50, "timeout": 0, "allowed_updates": []string{"message", "chat_member", "chat_join_request"}}, &updates); err != nil {
		return err
	}
	for _, update := range updates {
		if update.ID < offset {
			continue
		}
		if update.Member != nil && strconv.FormatInt(update.Member.Chat.ID, 10) == plugin.chat && (update.Member.New.Status == "member" || update.Member.New.Status == "administrator" || update.Member.New.Status == "restricted" && update.Member.New.IsMember) {
			current, found, err := plugin.inviteForTelegram(ctx, update.Member.New.User.ID)
			if err != nil {
				return err
			}
			if found {
				// Membership is already effective in Telegram. Persist consumption before
				// cleanup so a failed revocation cannot restore this website account's eligibility.
				current.Used = true
				current.Revoke = current.URL != "" && !current.Revoked
				if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
					return err
				}
				if current.Revoke {
					// Membership is confirmed even if cleanup fails. Pending revocation is
					// retained by the saved record and retried before a later poll.
					_ = plugin.revoke(ctx, &current)
				}
			}
		}
		if update.Join != nil && strconv.FormatInt(update.Join.Chat.ID, 10) == plugin.chat {
			// Bindings identify the requester even when Telegram omits invitation attribution.
			// An unbound holder of a forwarded group link has no admission claim.
			if err := plugin.resolveJoin(ctx, update.Join.From.ID); err != nil {
				if eventErr := plugin.event(ctx, "处理入群请求失败", err.Error()); eventErr != nil {
					return eventErr
				}
			}
		}
		if update.Message != nil && update.Message.Chat.Type == "private" &&
			update.Message.Chat.ID == update.Message.From.ID && !update.Message.From.Bot &&
			strings.HasPrefix(update.Message.Text, "/start") {
			if err := plugin.handleStart(ctx, update.Message); err != nil {
				// Report the failure before deciding whether to retain this update.
				// Persisted ownership lets a later Start resume without creating a second binding.
				if eventErr := plugin.event(ctx, "机器人绑定处理失败", err.Error()); eventErr != nil {
					return eventErr
				}
				var rejection *telegramRejection
				if !errors.As(err, &rejection) {
					return err
				}
				// A definite refusal such as a blocked private conversation cannot be
				// retried usefully in this batch. Advance so other users' updates still run.
			}
		}
		if update.Message != nil && strconv.FormatInt(update.Message.Chat.ID, 10) == plugin.chat && !update.Message.From.Bot && update.Message.Date >= plugin.now().Unix()-120 && len(update.Message.Text) <= 12000 {
			// Persist progress before model calls because Telegram replies have no
			// idempotency key; a crash can omit a reply but cannot replay this request.
			if err := plugin.save(ctx, plugin.scoped("offset"), update.ID+1); err != nil {
				return err
			}
			if err := plugin.handleMessage(ctx, update.Message); err != nil {
				if err := plugin.event(ctx, "群消息处理失败", err.Error()); err != nil {
					return err
				}
			}
		}
		// Non-assistant handlers have finished before progress is persisted. Their
		// durable invitation flags remain available if offset persistence requires another poll.
		offset = update.ID + 1
		if err := plugin.save(ctx, plugin.scoped("offset"), offset); err != nil {
			return err
		}
	}
	return nil
}

func (plugin *Plugin) retryRevokes(ctx context.Context) error {
	rows, err := plugin.db.QueryContext(ctx, "SELECT value FROM records WHERE key LIKE ? AND json_extract(value,'$.revoke')=1 LIMIT 50", plugin.scoped("invite/%"))
	if err != nil {
		return err
	}
	var pending []Invite
	for rows.Next() {
		var raw string
		var current Invite
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err := decodeRecord(raw, &current); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, current)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, current := range pending {
		if err := plugin.revoke(ctx, &current); err != nil {
			return err
		}
	}
	return nil
}

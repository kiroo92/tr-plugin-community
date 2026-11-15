package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

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
			if err := plugin.save(ctx, plugin.scoped("retry"), plugin.now().Unix()+result.Parameters.Retry); err != nil {
				return err
			}
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
		Type     string `json:"type"`
		Username string `json:"username"`
	}
	if err := plugin.telegram(ctx, "getChat", map[string]any{"chat_id": plugin.chat}, &chat); err != nil {
		return err
	}
	if chat.Type != "supergroup" || chat.Username != "" {
		return errors.New("需要无公开用户名的私有超级群组")
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
type Message struct {
	ID   int64  `json:"message_id"`
	Date int64  `json:"date"`
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From  TGUser   `json:"from"`
	Reply *Message `json:"reply_to_message"`
}
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
		} `json:"new_chat_member"`
	} `json:"chat_member"`
}

func (plugin *Plugin) poll(ctx context.Context) error {
	var bot Bot
	found, err := plugin.load(ctx, plugin.scoped("bot"), &bot)
	if err != nil {
		return err
	}
	if !found || plugin.checkedAt == 0 || plugin.now().Unix()-plugin.checkedAt >= 300 {
		if err := plugin.checkBot(ctx); err != nil {
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
	if err := plugin.telegram(ctx, "getUpdates", map[string]any{"offset": offset, "limit": 50, "timeout": 0, "allowed_updates": []string{"message", "chat_member"}}, &updates); err != nil {
		return err
	}
	for _, update := range updates {
		if update.ID < offset {
			continue
		}
		if update.Member != nil && strconv.FormatInt(update.Member.Chat.ID, 10) == plugin.chat && update.Member.Invite.Link != "" && (update.Member.New.Status == "member" || update.Member.New.Status == "administrator" || update.Member.New.Status == "restricted" && update.Member.New.IsMember) {
			var raw string
			err := plugin.db.QueryRowContext(ctx, "SELECT value FROM records WHERE key LIKE ? AND json_extract(value,'$.url')=? LIMIT 1", plugin.scoped("invite/%"), update.Member.Invite.Link).Scan(&raw)
			if err == nil {
				var current Invite
				if err := decodeRecord(raw, &current); err != nil {
					return err
				}
				current.Used = true
				current.Revoke = true
				if err := plugin.save(ctx, plugin.inviteKey(current.UserID), current); err != nil {
					return err
				}
				_ = plugin.revoke(ctx, &current)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return errors.New("匹配入群邀请失败")
			}
		}
		if update.Message != nil && strconv.FormatInt(update.Message.Chat.ID, 10) == plugin.chat && !update.Message.From.Bot && update.Message.Date >= plugin.now().Unix()-120 && len(update.Message.Text) <= 12000 {
			// shortcut: 发送前推进 offset 避免崩溃重复回复；发送中崩溃可能漏一条，Telegram 不提供幂等键。
			if err := plugin.save(ctx, plugin.scoped("offset"), update.ID+1); err != nil {
				return err
			}
			if err := plugin.handleMessage(ctx, update.Message); err != nil {
				if err := plugin.event(ctx, "群消息处理失败", err.Error()); err != nil {
					return err
				}
			}
		}
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

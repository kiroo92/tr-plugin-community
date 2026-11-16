package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// commands.go routes Telegram slash commands and publishes their private and
// group menus. Commands use the requesting sender for account access and AI
// history changes. Reply targets select an identity only for the public ID
// lookup, where sender_chat takes precedence over Telegram's synthetic user
// attached to anonymous administrator or channel messages.
//
// Private onboarding delegates to handleStart so binding and delivery retain
// their persistent recovery rules. Group replies keep the original topic and
// message reference. Menu synchronization records a fingerprint after both
// scopes succeed; partial failures leave the fingerprint unchanged and permit
// the next connection check to repeat the idempotent menu requests.

// botCommand describes one slash command displayed by Telegram's command menu.
type botCommand struct {
	Command     string `json:"command"`     // slash-free command name
	Description string `json:"description"` // displayed Chinese command description
}

// syncCommands publishes the private and configured-group command menus after
// bot permissions have been checked. Callers hold plugin.mu. A persisted content
// fingerprint skips unchanged menus across restarts, including repeated Start
// requests that recheck the bot. Both remote writes must finish before the
// fingerprint is committed; a failure after either write allows the complete
// pair to be retried during a later connection check.
func (plugin *Plugin) syncCommands(ctx context.Context) error {
	menus := []map[string]any{
		{"scope": map[string]any{"type": "all_private_chats"}, "commands": commandsFor("private")},
		{"scope": map[string]any{"type": "chat", "chat_id": plugin.chat}, "commands": commandsFor("supergroup")},
	}
	raw, err := json.Marshal(menus)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	var saved string
	if _, err := plugin.load(ctx, plugin.scoped("commands"), &saved); err != nil {
		return err
	}
	if saved == fingerprint {
		return nil
	}
	for _, menu := range menus {
		if err := plugin.telegram(ctx, "setMyCommands", menu, nil); err != nil {
			return err
		}
	}
	// Commit after both scopes succeed. A partial update can safely repeat the
	// same menu writes, while a premature marker would leave one scope outdated.
	return plugin.save(ctx, plugin.scoped("commands"), fingerprint)
}

// commandsFor returns the menu appropriate to a private or configured group chat.
// Private commands expose account operations; group commands offer public queries
// and the requesting user's own conversation reset.
func commandsFor(chatType string) []botCommand {
	if chatType == "private" {
		return []botCommand{
			{"start", "绑定网站账号或继续领取群邀请"},
			{"help", "查看命令和使用说明"},
			{"id", "查看 Telegram ID"},
			{"status", "查看本人的绑定与入群资格"},
			{"invite", "重新发送本人的群邀请"},
			{"reset", "清除本人的 AI 对话上下文"},
		}
	}
	return []botCommand{
		{"help", "查看命令和使用说明"},
		{"id", "查看自己或回复对象的 Telegram ID"},
		{"status", "查看社群功能运行状态"},
		{"channels", "更新群内公开渠道状态图"},
		{"reset", "清除本人的 AI 对话上下文"},
	}
}

// handleCommand handles slash-prefixed text before any assistant trigger runs.
// Callers supply a recent configured-group message or an authenticated private
// sender and hold plugin.mu. The boolean consumes all slash-prefixed messages,
// including commands addressed to another bot, so they cannot become AI prompts.
//
// A bot suffix is checked against the saved username. Private Start and invite
// commands normalize their spelling before delegating to the durable onboarding
// handler; reply targets never select another user's binding or AI history.
func (plugin *Plugin) handleCommand(ctx context.Context, message *Message) (bool, error) {
	parts := strings.Fields(message.Text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return false, nil
	}
	name, suffix, addressed := strings.Cut(strings.TrimPrefix(parts[0], "/"), "@")
	if addressed {
		var bot Bot
		if _, err := plugin.load(ctx, plugin.scoped("bot"), &bot); err != nil {
			return true, err
		}
		if bot.Username == "" || !strings.EqualFold(suffix, bot.Username) {
			return true, nil
		}
	}
	name = strings.ToLower(name)
	if len(parts) > 1 && name != "start" {
		return true, plugin.commandReply(ctx, message, "该命令不接受参数。发送 /help 查看用法。")
	}
	switch name {
	case "start", "invite":
		if message.Chat.Type != "private" {
			return true, plugin.commandReply(ctx, message, "请在网站社群中心打开本人的专属机器人链接，私聊点击 Start 完成绑定。已绑定后可私聊发送 /invite 重新获取群邀请。")
		}
		normalized := *message
		normalized.Text = "/start"
		if len(parts) > 1 {
			normalized.Text += " " + strings.Join(parts[1:], " ")
		}
		return true, plugin.handleStart(ctx, &normalized)
	case "help":
		var text strings.Builder
		text.WriteString("社群中心机器人命令\n")
		for _, command := range commandsFor(message.Chat.Type) {
			fmt.Fprintf(&text, "/%s：%s\n", command.Command, command.Description)
		}
		text.WriteString("\n在群里回复某人的消息，再发送 /id，即可查询对方的 Telegram ID。也支持 /id@机器人用户名。")
		if message.Chat.Type == "private" {
			text.WriteString("\n首次绑定请从网站的专属机器人链接点击 Start。入口和群邀请长期有效，入群后群邀请撤销。")
		} else {
			text.WriteString("\n绑定状态和群邀请请私聊使用 /status、/invite。/reset 只清除发送者本人的对话。")
		}
		return true, plugin.commandReply(ctx, message, text.String())
	case "id":
		target := message
		if message.Reply != nil {
			target = message.Reply
		}
		text := "Telegram 未提供这条消息的发送者 ID。"
		if target.SenderChat != nil {
			// Telegram supplies a synthetic From for anonymous chat senders. Prefer
			// sender_chat so that synthetic identifier cannot be mistaken for a person's ID.
			text = fmt.Sprintf("发送方聊天 ID：%d\n该消息以群组或频道身份发送，Telegram 未提供个人用户 ID。", target.SenderChat.ID)
		} else if target.From.ID > 0 {
			text = fmt.Sprintf("Telegram 用户 ID：%d", target.From.ID)
		}
		text += fmt.Sprintf("\n当前聊天 ID：%d", message.Chat.ID)
		return true, plugin.commandReply(ctx, message, text)
	case "status":
		return true, plugin.commandStatus(ctx, message)
	case "channels":
		if message.Chat.Type == "private" {
			return true, plugin.commandReply(ctx, message, "请在交流群发送 /channels 查询公开渠道状态图。")
		}
		if !plugin.settings.Channels {
			return true, plugin.commandReply(ctx, message, "管理员已暂停渠道状态播报。")
		}
		if err := plugin.broadcast(ctx, true); err != nil {
			return true, plugin.commandReply(ctx, message, err.Error())
		}
		return true, plugin.commandReply(ctx, message, "群内公开渠道状态图已更新。")
	case "reset":
		if message.SenderChat != nil || message.From.ID <= 0 {
			return true, plugin.commandReply(ctx, message, "请切换到个人身份发送 /reset，或私聊机器人清除本人的对话。")
		}
		key := plugin.scoped("history/" + strconv.FormatInt(message.From.ID, 10))
		if _, err := plugin.db.ExecContext(ctx, "DELETE FROM records WHERE key=?", key); err != nil {
			return true, err
		}
		return true, plugin.commandReply(ctx, message, "已清除你的 AI 对话上下文，下次提问将开始新对话。")
	default:
		return true, plugin.commandReply(ctx, message, "未知命令。发送 /help 查看可用命令。")
	}
}

// commandStatus replies with public switches in groups and the sender's own
// binding in private chats. Callers hold plugin.mu. Private eligibility is
// freshly checked, and consumed claims describe an outcome requiring group
// confirmation because Telegram admission responses can be lost in transit.
// A revoked entry takes precedence over its consumption state, followed by the
// invitation switch and current eligibility, so the response names the next
// available action for this particular website account.
func (plugin *Plugin) commandStatus(ctx context.Context, message *Message) error {
	if message.Chat.Type != "private" {
		state := map[bool]string{true: "已开启", false: "已暂停"}
		text := fmt.Sprintf("群组：%s\n邀请领取：%s\nAI 群聊助手：%s\n渠道状态播报：%s\n\n本人的绑定和入群资格请私聊发送 /status。",
			plugin.settings.Group, state[plugin.settings.Invites], state[plugin.settings.AI], state[plugin.settings.Channels])
		return plugin.commandReply(ctx, message, text)
	}
	current, found, err := plugin.inviteForTelegram(ctx, message.From.ID)
	if err != nil {
		return err
	}
	if !found {
		return plugin.commandReply(ctx, message, "你尚未绑定网站账号。请登录网站社群中心，打开本人的专属机器人链接并点击 Start。")
	}
	qualification, err := plugin.eligible(ctx, current.UserID)
	if err != nil {
		return err
	}
	qualificationText := "未满足，请先绑定 LinuxDO 或完成余额充值"
	if qualification.Source != "" {
		qualificationText = "已满足"
	}
	entry := "可发送 /invite 获取群邀请"
	if current.StartToken == "" {
		entry = "入口已撤销，请回网站重新领取机器人链接"
	} else if current.Used {
		entry = "入群资格已使用，实际入群结果请在群内确认；如有疑问请联系管理员"
	} else if !plugin.settings.Invites {
		entry = "管理员已暂停社群入口，请稍后再试"
	} else if qualification.Source == "" {
		entry = "恢复网站入群资格后，可发送 /invite 获取群邀请"
	}
	text := fmt.Sprintf("Telegram 用户 ID：%d\n已绑定网站账号：%d\n入群资格：%s\n%s", message.From.ID, current.UserID, qualificationText, entry)
	return plugin.commandReply(ctx, message, text)
}

// commandReply sends plain command output back to the originating chat.
// Replies retain their message and forum topic when supplied by Telegram, so
// group ID queries remain beside the command that requested them.
func (plugin *Plugin) commandReply(ctx context.Context, message *Message, text string) error {
	input := map[string]any{"chat_id": message.Chat.ID, "text": text}
	if message.ID > 0 {
		input["reply_parameters"] = map[string]any{"message_id": message.ID, "allow_sending_without_reply": true}
	}
	if message.ThreadID > 0 {
		input["message_thread_id"] = message.ThreadID
	}
	return plugin.telegram(ctx, "sendMessage", input, nil)
}

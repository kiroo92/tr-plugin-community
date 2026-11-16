package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIDCommandReplies exercises real polling with self, reply, and anonymous
// sender queries. Replies identify the bound website account, keep their forum topic,
// and continue working when invitation and AI features are disabled.
func TestIDCommandReplies(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, binding := range []struct {
		telegram int64 // Telegram sender identifier
		website  int64 // bound website identifier
	}{{777, 9}, {4503599627370495, 42}, {888, 43}, {889, 44}} {
		if err := f.plugin.save(ctx, f.plugin.inviteKey(binding.website), Invite{UserID: binding.website, TelegramID: binding.telegram}); err != nil {
			t.Fatal(err)
		}
	}
	for index, scenario := range []struct {
		name   string   // identity query scenario
		reply  *Message // optional replied message
		text   string   // incoming slash command
		want   string   // expected identity response
		absent string   // forbidden synthetic user identity
	}{
		{name: "self", text: "/id", want: "网站用户 ID：9"},
		{name: "reply", text: "/id", reply: &Message{From: TGUser{ID: 4503599627370495}}, want: "网站用户 ID：42", absent: "网站用户 ID：9"},
		{name: "addressed", text: "/id@TestBot", reply: &Message{From: TGUser{ID: 888}}, want: "网站用户 ID：43"},
		{name: "username case", text: "/id@tEsTbOt", reply: &Message{From: TGUser{ID: 889}}, want: "网站用户 ID：44"},
		{name: "anonymous", text: "/id", reply: &Message{From: TGUser{ID: 1087968824}, SenderChat: &TGChat{ID: -100888, Type: "supergroup"}}, want: "无法查询绑定的网站用户 ID", absent: "1087968824"},
		{name: "channel", text: "/id", reply: &Message{SenderChat: &TGChat{ID: -100889, Type: "channel"}}, want: "无法查询绑定的网站用户 ID"},
		{name: "unbound", text: "/id", reply: &Message{From: TGUser{ID: 990}}, want: "尚未绑定网站账号"},
		{name: "sender unavailable", text: "/id", reply: &Message{}, want: "未提供该消息的发送者"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			message := &Message{
				ID:       int64(index + 10),
				Date:     f.clock.Unix(),
				ThreadID: 33,
				Text:     scenario.text,
				Chat:     TGChat{ID: -100123456, Type: "supergroup"},
				From:     TGUser{ID: 777},
				Reply:    scenario.reply,
			}
			f.updates = []Update{{ID: int64(index + 1000), Message: message}}
			if err := f.plugin.poll(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.messages) != index+1 {
				t.Fatal("ID command produced no reply")
			}
			got := f.messages[index]
			text := got["text"].(string)
			if !strings.Contains(text, scenario.want) || strings.Contains(text, "Telegram 用户 ID：") || strings.Contains(text, "当前聊天 ID：") || scenario.absent != "" && strings.Contains(text, scenario.absent) {
				t.Fatalf("wrong ID response: %s", text)
			}
			parameters, ok := got["reply_parameters"].(map[string]any)
			if !ok || parameters["message_id"] != float64(message.ID) || got["message_thread_id"] != float64(33) || got["chat_id"] != float64(-100123456) {
				t.Fatalf("reply lost its chat, message, or topic: %+v", got)
			}
		})
	}
	if f.calls["completions"] != 0 || f.calls["createChatInviteLink"] != 0 {
		t.Fatal("ID lookup triggered an unrelated feature")
	}
}

// TestCommandRouting isolates commands from AI mentions and bot-reply triggers.
// Other bots' commands, stale messages, unrelated groups, and bot senders are
// ignored while a normal mention still reaches the assistant.
func TestCommandRouting(t *testing.T) {
	f := setup(t)
	f.plugin.settings.AI = true
	f.plugin.settings.Endpoint = f.server.URL + "/v1"
	for index, text := range []string{"/id@OtherBot @TestBot", "/help@TestBot", "/unknown@TestBot", "/id 123", "/starter"} {
		message := &Message{
			ID:    int64(index + 1),
			Date:  f.clock.Unix(),
			Text:  text,
			Chat:  TGChat{ID: -100123456, Type: "supergroup"},
			From:  TGUser{ID: 777},
			Reply: &Message{From: TGUser{ID: 12345, Bot: true}},
		}
		f.updates = []Update{{ID: int64(index + 1000), Message: message}}
		if err := f.plugin.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls["completions"] != 0 || len(f.messages) != 4 {
		t.Fatal("commands reached AI or another bot received a response")
	}
	for index, scenario := range []string{"stale", "other group", "bot"} {
		message := &Message{ID: 20, Date: f.clock.Unix(), Text: "/id", Chat: TGChat{ID: -100123456, Type: "supergroup"}, From: TGUser{ID: 777}}
		switch scenario {
		case "stale":
			message.Date = f.clock.Add(-3 * time.Minute).Unix()
		case "other group":
			message.Chat.ID = -100555
		case "bot":
			message.From.Bot = true
		}
		f.updates = []Update{{ID: int64(index + 2000), Message: message}}
		if err := f.plugin.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.messages) != 4 {
		t.Fatal("unsupported group update received a command response")
	}
	f.updates = []Update{{ID: 3000, Message: &Message{ID: 30, Date: f.clock.Unix(), Text: "@TestBot 你好", Chat: TGChat{ID: -100123456, Type: "supergroup"}, From: TGUser{ID: 777}}}}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls["completions"] != 1 {
		t.Fatal("normal assistant trigger stopped working")
	}
}

// TestCommandMenus checks private and target-group slash menus through connection
// verification. Partial failures leave normal polling available, retry both
// scopes, and cache successful menus across subsequent process restarts.
func TestCommandMenus(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.fail = "setMyCommands"
	f.failCall = 2
	f.updates = []Update{{ID: 1000, Message: &Message{ID: 1, Date: f.clock.Unix(), Text: "/id", Chat: TGChat{ID: -100123456, Type: "supergroup"}, From: TGUser{ID: 777}}}}
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal("menu failure interrupted normal polling", err)
	}
	var saved string
	if found, err := f.plugin.load(ctx, f.plugin.scoped("commands"), &saved); err != nil || found {
		t.Fatal("partially applied menus were marked complete", err)
	}
	if len(f.messages) != 1 {
		t.Fatal("ID query failed while menu registration was unavailable")
	}
	f.fail = ""
	f.clock = f.clock.Add(5 * time.Minute)
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls["setMyCommands"] != 4 || len(f.menus) != 3 {
		t.Fatal("both menu scopes were not retried")
	}
	for index, want := range []string{"all_private_chats", "chat"} {
		menu := f.menus[index+1]
		scope := menu["scope"].(map[string]any)
		if scope["type"] != want || want == "chat" && scope["chat_id"] != "-100123456" {
			t.Fatalf("wrong menu scope: %+v", scope)
		}
		commands := menu["commands"].([]any)
		foundID := false
		for _, raw := range commands {
			command := raw.(map[string]any)
			foundID = foundID || command["command"] == "id"
		}
		if !foundID {
			t.Fatal("ID query missing from slash menu")
		}
	}
	f.restart()
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls["setMyCommands"] != 4 {
		t.Fatal("unchanged command menus were registered again")
	}
}

// TestAccountCommands verifies that private status and invite operate on the
// sender's binding. Group status omits account data, repeated invites reuse a
// permanent link, and a reply cannot clear someone else's AI history.
func TestAccountCommands(t *testing.T) {
	f := setup(t)
	f.bindAccount(777)
	ctx := context.Background()
	current, _, err := f.plugin.inviteForTelegram(ctx, 777)
	if err != nil {
		t.Fatal(err)
	}
	message := &Message{ID: 1, Text: "/status", Chat: TGChat{ID: 777, Type: "private"}, From: TGUser{ID: 777}}
	if handled, err := f.plugin.handleCommand(ctx, message); err != nil || !handled {
		t.Fatal("private status command failed", err)
	}
	status := f.messages[len(f.messages)-1]["text"].(string)
	if !strings.Contains(status, "已绑定网站账号：9") || !strings.Contains(status, "入群资格：已满足") || strings.Contains(status, current.StartToken) {
		t.Fatal("private status lost its binding or exposed an entry token")
	}
	message.Text = "/invite@TestBot"
	for range 2 {
		if _, err := f.plugin.handleCommand(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls["createChatInviteLink"] != 1 {
		t.Fatal("repeated invite command replaced a permanent group invitation")
	}
	message.From.ID = 778
	message.Chat.ID = 778
	if _, err := f.plugin.handleCommand(ctx, message); err != nil {
		t.Fatal(err)
	}
	if f.calls["createChatInviteLink"] != 1 || strings.Contains(f.messages[len(f.messages)-1]["text"].(string), "https://t.me/+") {
		t.Fatal("unbound sender received another account's invitation")
	}
	f.linux = false
	message.From.ID = 777
	message.Chat.ID = 777
	message.Text = "/status"
	if _, err := f.plugin.handleCommand(ctx, message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.messages[len(f.messages)-1]["text"].(string), "入群资格：未满足") {
		t.Fatal("status did not recheck current eligibility")
	}
	message.Chat = TGChat{ID: -100123456, Type: "supergroup"}
	message.Reply = &Message{From: TGUser{ID: 888}}
	if _, err := f.plugin.handleCommand(ctx, message); err != nil {
		t.Fatal(err)
	}
	public := f.messages[len(f.messages)-1]["text"].(string)
	if !strings.Contains(public, "AI 群聊助手") || strings.Contains(public, "网站账号：") || strings.Contains(public, current.StartToken) {
		t.Fatal("group status exposed private account details")
	}
	for _, id := range []int64{777, 888} {
		if err := f.plugin.save(ctx, f.plugin.scoped(fmt.Sprintf("history/%d", id)), []ChatTurn{{Role: "user", Content: "private history"}}); err != nil {
			t.Fatal(err)
		}
	}
	message.Text = "/reset"
	if _, err := f.plugin.handleCommand(ctx, message); err != nil {
		t.Fatal(err)
	}
	var history []ChatTurn
	if found, err := f.plugin.load(ctx, f.plugin.scoped("history/777"), &history); err != nil || found {
		t.Fatal("requesting user's history survived reset", err)
	}
	if found, err := f.plugin.load(ctx, f.plugin.scoped("history/888"), &history); err != nil || !found || len(history) != 1 {
		t.Fatal("reply target's history was changed", err)
	}
}

// TestChannelCommand checks disabled, private, successful, and cooling-down
// channel queries. Users receive a readable response and repeated commands
// respect the existing shared group refresh cooldown.
func TestChannelCommand(t *testing.T) {
	f := setup(t)
	message := &Message{ID: 1, Text: "/channels", Chat: TGChat{ID: -100123456, Type: "supergroup"}, From: TGUser{ID: 777}}
	for index, want := range []string{"已暂停", "已更新", "冷却中", "请在交流群"} {
		if index == 1 {
			f.plugin.settings.Channels = true
		}
		if index == 3 {
			message.Chat = TGChat{ID: 777, Type: "private"}
		}
		if handled, err := f.plugin.handleCommand(context.Background(), message); err != nil || !handled {
			t.Fatal("channel command failed", err)
		}
		if !strings.Contains(f.messages[len(f.messages)-1]["text"].(string), want) {
			t.Fatalf("wrong channel command response: %+v", f.messages[len(f.messages)-1])
		}
	}
	if f.calls["sendPhoto"] != 1 || f.calls["editMessageMedia"] != 0 {
		t.Fatal("channel command bypassed refresh cooldown")
	}
}

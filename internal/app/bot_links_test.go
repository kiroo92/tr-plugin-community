package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// bot_links_test.go exercises permanent website entry links through real plugin
// handlers and serialized Telegram updates served by the local fixture. The
// tests follow token publication, private Start ownership, invitation delivery,
// and admission when Telegram omits the original invitation attribution.

// TestBotStartAdmission binds the website claimant after a private Start update.
// The bot sends an invitation, rejects a forwarded-link holder, and approves
// the bound requester whose join event carries no invitation attribution.
func TestBotStartAdmission(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	f.groupName = "moonup_chat"
	f.joinByRequest = true
	response := f.request("POST", "/invite", "", "user")
	mustStatus(t, response, 200)
	var status struct {
		Invite Invite `json:"invite"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	entry, err := url.Parse(status.Invite.URL)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Host != "t.me" || entry.Path != "/TestBot" || len(entry.Query().Get("start")) != 43 {
		t.Fatal("website did not publish a bot start link")
	}
	start := fmt.Sprintf(`{"update_id":500,"message":{"message_id":1,"chat":{"id":777,"type":"private"},"from":{"id":777},"text":%q}}`, "/start "+entry.Query().Get("start"))
	var update Update
	if err := json.Unmarshal([]byte(start), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var saved Invite
	if _, err := f.plugin.load(context.Background(), f.plugin.inviteKey(9), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.TelegramID != 777 || saved.BoundAt == 0 || saved.StartToken == "" || saved.URL == "" {
		t.Fatalf("Start did not persist binding and invitation: %+v", saved)
	}
	if len(f.messages) != 1 || f.messages[0]["chat_id"] != float64(777) || !strings.Contains(f.messages[0]["text"].(string), saved.URL) {
		t.Fatal("bot did not privately send the group invitation")
	}
	if f.calls["completions"] != 0 {
		t.Fatal("private Start reached the group assistant")
	}
	f.restart()
	for index, requester := range []int64{778, 777} {
		raw := fmt.Sprintf(`{"update_id":%d,"chat_join_request":{"chat":{"id":-100123456},"from":{"id":%d}}}`, 501+index, requester)
		if err := json.Unmarshal([]byte(raw), &update); err != nil {
			t.Fatal(err)
		}
		f.updates = []Update{update}
		if err := f.plugin.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls["declineChatJoinRequest"] != 1 || len(f.admitted) != 1 || f.admitted[0] != 777 {
		t.Fatalf("admission did not use the bound identity: %+v", f.admitted)
	}
	response = f.request("GET", "/status", "", "user")
	mustStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Invite.URL != entry.String() || status.Invite.TelegramID != 777 || !status.Invite.Used || status.Invite.StartToken != "" {
		t.Fatal("website did not retain the permanent bot entry and binding status")
	}
	admin := f.request("GET", "/admin/invites", "", "admin")
	mustStatus(t, admin, 200)
	if strings.Contains(admin.Body.String(), saved.StartToken) || strings.Contains(admin.Body.String(), saved.URL) {
		t.Fatal("administrator list exposed a bearer token or group invitation")
	}
}

// TestStartOwnership protects both directions of the website-to-Telegram binding.
// A redeemed token cannot change owners, and a Telegram account cannot redeem
// another website account's token even after process restart.
func TestStartOwnership(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	ctx := context.Background()
	first, err := f.plugin.issueBotLink(ctx, 9, "test eligibility")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.plugin.issueBotLink(ctx, 10, "test eligibility")
	if err != nil {
		t.Fatal(err)
	}
	message := &Message{From: TGUser{ID: 777}, Text: "/start " + first.StartToken}
	message.Chat.ID = 777
	message.Chat.Type = "private"
	if err := f.plugin.handleStart(ctx, message); err != nil {
		t.Fatal(err)
	}
	f.restart()
	f.plugin.settings.Invites = true
	message.From.ID = 778
	message.Chat.ID = 778
	if err := f.plugin.handleStart(ctx, message); err != nil {
		t.Fatal(err)
	}
	message.From.ID = 777
	message.Chat.ID = 777
	message.Text = "/start " + second.StartToken
	if err := f.plugin.handleStart(ctx, message); err != nil {
		t.Fatal(err)
	}
	owner, found, err := f.plugin.inviteForTelegram(ctx, 777)
	if err != nil || !found || owner.UserID != 9 {
		t.Fatal("existing Telegram ownership changed", err)
	}
	if _, found, err := f.plugin.inviteForTelegram(ctx, 778); err != nil || found {
		t.Fatal("a forwarded token acquired a second Telegram owner", err)
	}
	if f.calls["createChatInviteLink"] != 1 || len(f.messages) != 3 {
		t.Fatal("ownership conflicts delivered another invitation")
	}
	if !strings.Contains(f.messages[1]["text"].(string), "已绑定其他") || !strings.Contains(f.messages[2]["text"].(string), "另一个网站账号") {
		t.Fatal("ownership conflicts were not explained to private senders")
	}
}

// TestStartRecovery replays a private Start whose invitation message failed.
// Binding and invitation creation survive restart, and the replay sends the same
// invitation before advancing the Telegram update offset.
func TestStartRecovery(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	ctx := context.Background()
	if err := f.plugin.save(ctx, "settings", f.plugin.settings); err != nil {
		t.Fatal(err)
	}
	current, err := f.plugin.issueBotLink(ctx, 9, "test eligibility")
	if err != nil {
		t.Fatal(err)
	}
	var update Update
	raw := fmt.Sprintf(`{"update_id":700,"message":{"chat":{"id":777,"type":"private"},"from":{"id":777},"text":%q}}`, "/start "+current.StartToken)
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	f.fail = "sendMessage"
	if err := f.plugin.poll(ctx); err == nil {
		t.Fatal("private delivery failure was not reported")
	}
	var offset int64
	if _, err := f.plugin.load(ctx, f.plugin.scoped("offset"), &offset); err != nil || offset > 700 {
		t.Fatal("failed delivery advanced the update offset", err)
	}
	f.fail = ""
	f.restart()
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls["createChatInviteLink"] != 1 || len(f.messages) != 1 {
		t.Fatal("recovery created another group invitation")
	}
	if _, err := f.plugin.load(ctx, f.plugin.scoped("offset"), &offset); err != nil || offset != 701 {
		t.Fatal("successful replay did not advance the offset", err)
	}
	f.clock = f.clock.Add(11 * time.Minute)
	message := &Message{From: TGUser{ID: 777}, Text: "/start"}
	message.Chat.ID = 777
	message.Chat.Type = "private"
	if err := f.plugin.handleStart(ctx, message); err != nil {
		t.Fatal(err)
	}
	if f.calls["createChatInviteLink"] != 2 {
		t.Fatal("bare Start did not renew the internal group invitation")
	}
	stored, found, err := f.plugin.inviteForTelegram(ctx, 777)
	if err != nil || !found || stored.StartToken != current.StartToken {
		t.Fatal("group invitation renewal changed the permanent token", err)
	}
}

// TestStartGuards checks invalid payloads, group messages, and changed eligibility.
// Those inputs cannot establish a binding or issue a group invitation, while
// the permanent website token remains available for a later valid attempt.
func TestStartGuards(t *testing.T) {
	for _, scenario := range []string{"invalid payload", "group start", "eligibility lost", "entry revoked", "paused"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			f.plugin.settings.Invites = true
			ctx := context.Background()
			current, err := f.plugin.issueBotLink(ctx, 9, "test eligibility")
			if err != nil {
				t.Fatal(err)
			}
			message := &Message{From: TGUser{ID: 777}, Text: "/start " + current.StartToken}
			message.Chat.ID = 777
			message.Chat.Type = "private"
			switch scenario {
			case "invalid payload":
				message.Text = "/start invalid"
			case "group start":
				message.Chat.ID = -100123456
				message.Chat.Type = "supergroup"
			case "eligibility lost":
				f.linux = false
				f.orders = nil
			case "entry revoked":
				mustStatus(t, f.request("POST", "/admin/revoke", `{"user_id":9}`, "admin"), 200)
			case "paused":
				f.plugin.settings.Invites = false
			}
			if err := f.plugin.handleStart(ctx, message); err != nil {
				t.Fatal(err)
			}
			if _, found, err := f.plugin.inviteForTelegram(ctx, 777); err != nil || found {
				t.Fatal("guarded input established a binding", err)
			}
			if f.calls["createChatInviteLink"] != 0 {
				t.Fatal("guarded input issued an invitation")
			}
		})
	}
}

// TestRejectedStartDeliveryKeepsPolling processes a blocked private conversation
// followed by another user's Start. The refusal advances its update offset,
// while the next user still receives an invitation and both bindings persist.
func TestRejectedStartDeliveryKeepsPolling(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	ctx := context.Background()
	first, err := f.plugin.issueBotLink(ctx, 9, "test eligibility")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.plugin.issueBotLink(ctx, 10, "test eligibility")
	if err != nil {
		t.Fatal(err)
	}
	var update Update
	raw := fmt.Sprintf(`{"update_id":800,"message":{"chat":{"id":777,"type":"private"},"from":{"id":777},"text":%q}}`, "/start "+first.StartToken)
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	f.fail = "sendMessage"
	f.failStatus = 403
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal("definite private refusal blocked polling", err)
	}
	var offset int64
	if _, err := f.plugin.load(ctx, f.plugin.scoped("offset"), &offset); err != nil || offset != 801 {
		t.Fatal("blocked conversation retained its update", err)
	}
	f.fail = ""
	raw = fmt.Sprintf(`{"update_id":801,"message":{"chat":{"id":778,"type":"private"},"from":{"id":778},"text":%q}}`, "/start "+second.StartToken)
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.messages) != 1 || f.messages[0]["chat_id"] != float64(778) {
		t.Fatal("another private sender did not receive its invitation")
	}
	for _, telegramID := range []int64{777, 778} {
		if _, found, err := f.plugin.inviteForTelegram(ctx, telegramID); err != nil || !found {
			t.Fatal("private delivery outcome lost a binding", err)
		}
	}
}

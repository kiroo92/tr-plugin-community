package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/manifest"
	pluginpb "github.com/TokenFlux/TokenRouter/plugin-sdk/go/pluginpb/v1"
	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/plugintest"
	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
)

type fixture struct {
	t             *testing.T
	host          *plugintest.Harness
	plugin        *Plugin
	server        *httptest.Server
	clock         time.Time
	linux         bool
	orders        []*pluginpb.Order
	catalog       []byte
	catalogError  bool
	updates       []Update
	calls         map[string]int
	fail          string
	modelBody     map[string]any
	groupType     string
	groupName     string
	joinByRequest bool
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, host: plugintest.New(t), clock: time.Now(), linux: true, calls: map[string]int{}, catalog: []byte(`{"code":0,"data":[{"id":7,"name":"公开测试分组","display_brand":"Claude","availability":{"availability_rate":0.9,"last_status":"success","days":[{"availability_rate":1},{"availability_rate":null}]}}]}`)}
	f.host.Env.Config, _ = sdk.NewConfig([]byte(`{"bot_token":"12345:test_secret_123456","chat_id":"-100123456","ai_key":"fake-model-key"}`))
	f.host.Env.Permissions = []string{"user.read", "user.identities", "payment.read", "catalog.read"}
	f.host.Server.Hook = func(_ context.Context, method string, input any) (any, error, bool) {
		permission := map[string]string{"GetUser": "user.read", "ListUserIdentities": "user.identities", "ListUserOrders": "payment.read", "ListModels": "catalog.read"}[method]
		if permission != "" {
			granted := false
			for _, item := range f.host.Env.Permissions {
				if item == permission {
					granted = true
				}
			}
			if !granted {
				return nil, errors.New("permission denied"), true
			}
		}
		switch method {
		case "GetUser":
			return &pluginpb.User{Id: input.(*pluginpb.GetUserRequest).UserId, Status: "active"}, nil, true
		case "ListUserIdentities":
			var identities []*pluginpb.UserIdentity
			if f.linux {
				identities = []*pluginpb.UserIdentity{{ProviderType: "linuxdo", Subject: "42"}}
			}
			return &pluginpb.ListUserIdentitiesResponse{Identities: identities}, nil, true
		case "ListUserOrders":
			request := input.(*pluginpb.ListUserOrdersRequest)
			if request.Status != "COMPLETED" || request.PageSize != 50 {
				t.Error("unbounded orders")
			}
			first := min(int(request.Page-1)*50, len(f.orders))
			last := min(first+50, len(f.orders))
			return &pluginpb.ListUserOrdersResponse{Orders: f.orders[first:last], Total: int64(len(f.orders))}, nil, true
		case "ListModels":
			if f.catalogError {
				return nil, errors.New("failed"), true
			}
			return &pluginpb.ListModelsResponse{ModelsJson: f.catalog}, nil, true
		}
		return nil, nil, false
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.remote))
	t.Cleanup(f.server.Close)
	f.restart()
	return f
}

func (f *fixture) restart() {
	f.t.Helper()
	if f.plugin != nil {
		if err := f.plugin.Shutdown(context.Background()); err != nil {
			f.t.Fatal(err)
		}
	}
	plugin := New()
	plugin.telegramURL = f.server.URL
	plugin.now = func() time.Time { return f.clock }
	if err := plugin.Init(context.Background(), f.host.Env); err != nil {
		f.t.Fatal(err)
	}
	plugin.cancel()
	<-plugin.done
	f.plugin = plugin
	f.t.Cleanup(func() {
		if err := plugin.Shutdown(context.Background()); err != nil {
			f.t.Error(err)
		}
	})
}
func (f *fixture) remote(writer http.ResponseWriter, request *http.Request) {
	method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
	f.calls[method]++
	if method == f.fail {
		sdk.WriteJSON(writer, 500, map[string]any{"ok": false, "error_code": 500})
		return
	}
	var result any = true
	switch method {
	case "getMe":
		result = Bot{ID: 12345, Username: "TestBot"}
	case "getWebhookInfo":
		result = map[string]string{"url": ""}
	case "getChat":
		kind := f.groupType
		if kind == "" {
			kind = "supergroup"
		}
		result = map[string]any{"type": kind, "username": f.groupName, "join_by_request": f.joinByRequest}
	case "getChatMember":
		result = map[string]any{"status": "administrator", "can_invite_users": true}
	case "createChatInviteLink":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if input["member_limit"] != float64(1) || input["creates_join_request"] != nil {
			f.t.Error("must create direct single-member invite")
		}
		result = map[string]string{"invite_link": fmt.Sprintf("https://t.me/+test%d", f.calls[method])}
	case "getUpdates":
		result = f.updates
	case "declineChatJoinRequest":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if input["user_id"] == nil {
			f.t.Error("decline requires user_id")
		}
	case "sendPhoto", "editMessageMedia":
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			f.t.Error(err)
		}
		file, _, err := request.FormFile("photo")
		if err != nil {
			f.t.Error(err)
		} else {
			defer file.Close()
			if _, err := png.DecodeConfig(file); err != nil {
				f.t.Error("not PNG", err)
			}
		}
		result = map[string]int64{"message_id": 456}
	case "completions":
		if request.Header.Get("Authorization") != "Bearer fake-model-key" {
			f.t.Error("model authorization")
		}
		if err := json.NewDecoder(request.Body).Decode(&f.modelBody); err != nil {
			f.t.Error(err)
		}
		sdk.WriteJSON(writer, 200, map[string]any{"choices": []any{map[string]any{"message": ChatTurn{Role: "assistant", Content: "模型的真实测试回复"}}}, "usage": map[string]int{"total_tokens": 42}})
		return
	}
	sdk.WriteJSON(writer, 200, map[string]any{"ok": true, "result": result})
}
func (f *fixture) request(method, path, body, role string) *httptest.ResponseRecorder {
	f.t.Helper()
	caller := sdk.Caller{UserID: 9, Role: role}
	if role == "anonymous" {
		caller.UserID = 0
	}
	return f.host.Request(f.plugin.Handler(), httptest.NewRequest(method, path, strings.NewReader(body)), caller)
}
func mustStatus(t *testing.T, response *httptest.ResponseRecorder, code int) {
	t.Helper()
	if response.Code != code {
		t.Fatalf("status %d != %d: %s", response.Code, code, response.Body)
	}
}

func TestManifestAndDataDir(t *testing.T) {
	raw, err := os.ReadFile("../../plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := declaration.Validate(); err != nil {
		t.Fatal(err)
	}
	if declaration.MinHostVersion != "0.7.3" {
		t.Fatal("host version")
	}
	host := plugintest.New(t)
	host.Env.DataDir = ""
	if err := New().Init(context.Background(), host.Env); err == nil {
		t.Fatal("empty DataDir accepted")
	}
	host.Env.DataDir = "relative"
	if err := New().Init(context.Background(), host.Env); err == nil {
		t.Fatal("relative DataDir accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	host.Env.DataDir = file
	if err := New().Init(context.Background(), host.Env); err == nil {
		t.Fatal("file DataDir accepted")
	}
}

func TestPersistenceAndIsolation(t *testing.T) {
	first := setup(t)
	ctx := context.Background()
	first.plugin.settings.Group = "持久群组"
	if err := first.plugin.save(ctx, "settings", first.plugin.settings); err != nil {
		t.Fatal(err)
	}
	invite := Invite{UserID: 9, URL: "https://t.me/+persist", Expires: first.clock.Add(lifetime).Unix()}
	if err := first.plugin.save(ctx, first.plugin.inviteKey(9), invite); err != nil {
		t.Fatal(err)
	}
	if _, err := first.plugin.refreshChannels(ctx); err != nil {
		t.Fatal(err)
	}
	first.restart()
	var reopened Invite
	found, err := first.plugin.load(ctx, first.plugin.inviteKey(9), &reopened)
	if err != nil || !found || reopened.URL != invite.URL || first.plugin.settings.Group != "持久群组" {
		t.Fatal("state did not survive reopening", err)
	}
	mustStatus(t, first.request("GET", "/image", "", "admin"), 200)
	second := setup(t)
	if second.host.Env.DataDir == first.host.Env.DataDir || second.host.Env.DataDir == second.host.Env.StateDir {
		t.Fatal("directory isolation")
	}
	found, err = second.plugin.load(ctx, second.plugin.inviteKey(9), &reopened)
	if err != nil || found {
		t.Fatal("host data leaked")
	}
	for _, fixture := range []*fixture{first, second} {
		entries, err := os.ReadDir(fixture.host.Env.StateDir)
		if err != nil || len(entries) != 0 {
			t.Fatal("business data in StateDir")
		}
		if _, err := os.Stat(filepath.Join(fixture.host.Env.DataDir, "plugin.sqlite")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvitationsAndPermissions(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	if err := f.plugin.save(context.Background(), "settings", f.plugin.settings); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, f.request("GET", "/status", "", "anonymous"), 401)
	mustStatus(t, f.request("GET", "/admin/status", "", "user"), 403)
	mustStatus(t, f.request("POST", "/admin/clear", "", "user"), 403)
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	if f.calls["createChatInviteLink"] != 1 {
		t.Fatal("active link not reused")
	}
	f.clock = f.clock.Add(11 * time.Minute)
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	if f.calls["createChatInviteLink"] != 2 {
		t.Fatal("expired link not replaced")
	}
	var current Invite
	_, _ = f.plugin.load(context.Background(), f.plugin.inviteKey(9), &current)
	var update Update
	raw := fmt.Sprintf(`{"update_id":100,"chat_member":{"chat":{"id":-100123456},"invite_link":{"invite_link":%q},"new_chat_member":{"status":"member"}}}`, current.URL)
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, f.request("POST", "/invite", "", "user"), 409)
	if f.calls["revokeChatInviteLink"] != 1 {
		t.Fatal("used invite not revoked")
	}
	f.restart()
	mustStatus(t, f.request("POST", "/invite", "", "user"), 409)
	f.linux = false
	f.orders = nil
	mustStatus(t, f.request("GET", "/image", "", "user"), 403)
	if _, err := f.plugin.db.Exec("DELETE FROM records WHERE key=?", f.plugin.inviteKey(9)); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, f.request("POST", "/invite", "", "user"), 403)
	f.orders = []*pluginpb.Order{{Type: "subscription", Status: "COMPLETED", PaidUnix: 1, Amount: "100"}, {Type: "balance", Status: "COMPLETED", PaidUnix: 0, Amount: "1"}, {Type: "balance", Status: "COMPLETED", PaidUnix: 1, Amount: "0"}}
	mustStatus(t, f.request("POST", "/invite", "", "user"), 403)
	f.orders = append(f.orders, &pluginpb.Order{Type: "balance", Status: "COMPLETED", PaidUnix: 1, Amount: "0.00000001"})
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	f.host.Env.Permissions = nil
	mustStatus(t, f.request("GET", "/status", "", "user"), 502)
}

func TestPublicGroupAndJoinRequests(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	if err := f.plugin.save(context.Background(), "settings", f.plugin.settings); err != nil {
		t.Fatal(err)
	}
	f.groupName = "moonup_chat"
	f.joinByRequest = true
	mustStatus(t, f.request("POST", "/admin/check", "", "admin"), 200)
	var group GroupState
	if _, err := f.plugin.load(context.Background(), f.plugin.scoped("group"), &group); err != nil {
		t.Fatal(err)
	}
	if !group.Public || group.Username != "moonup_chat" || !group.Approval {
		t.Fatalf("公开群与审批状态未记录: %+v", group)
	}
	// 公开群 + 群级审批：机器人链接仍可直接创建并领取。
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	var current Invite
	if _, err := f.plugin.load(context.Background(), f.plugin.inviteKey(9), &current); err != nil {
		t.Fatal(err)
	}
	// 转发链接产生的入群请求被拒，且不能冒充领取成功。
	raw := fmt.Sprintf(`{"update_id":300,"chat_join_request":{"chat":{"id":-100123456},"from":{"id":777},"invite_link":{"invite_link":%q}}}`, current.URL)
	var update Update
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{update}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls["declineChatJoinRequest"] != 1 {
		t.Fatal("转发链接的入群请求未被拒绝")
	}
	var after Invite
	if _, err := f.plugin.load(context.Background(), f.plugin.inviteKey(9), &after); err != nil {
		t.Fatal(err)
	}
	if after.Used {
		t.Fatal("被拒绝的请求不应标记邀请已使用")
	}
	// 普通群仍被拒绝。
	f.groupType = "group"
	mustStatus(t, f.request("POST", "/admin/check", "", "admin"), 502)
}

func TestStatusReportsEachCondition(t *testing.T) {
	f := setup(t)
	status := func() map[string]any {
		t.Helper()
		response := f.request("GET", "/status", "", "user")
		mustStatus(t, response, 200)
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	payload := status()
	if payload["linuxdo"] != true || payload["recharge"] != nil {
		t.Fatalf("LinuxDO 满足时应跳过订单核验: %v", payload)
	}
	f.linux = false
	f.orders = nil
	payload = status()
	if payload["eligible"] != false || payload["linuxdo"] != false || payload["recharge"] != false {
		t.Fatalf("未满足条件时两项均应为否: %v", payload)
	}
	f.orders = []*pluginpb.Order{{Type: "balance", Status: "COMPLETED", PaidUnix: 1, Amount: "0.01"}}
	payload = status()
	if payload["eligible"] != true || payload["linuxdo"] != false || payload["recharge"] != true || payload["source"] != "余额充值" {
		t.Fatalf("充值满足时应单独标记: %v", payload)
	}
}

func TestUncertainCreationAndValidation(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	f.fail = "createChatInviteLink"
	mustStatus(t, f.request("POST", "/invite", "", "user"), 409)
	f.fail = ""
	mustStatus(t, f.request("POST", "/invite", "", "user"), 409)
	if f.calls["createChatInviteLink"] != 1 {
		t.Fatal("uncertain creation duplicated")
	}
	f.clock = f.clock.Add(11 * time.Minute)
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	invalid := f.plugin.settings
	invalid.Endpoint = "http://external.example/v1"
	raw, _ := json.Marshal(invalid)
	mustStatus(t, f.request("POST", "/admin/settings", string(raw), "admin"), 400)
	if f.plugin.settings.Endpoint == invalid.Endpoint {
		t.Fatal("invalid config applied")
	}
	mustStatus(t, f.request("POST", "/admin/settings", string(raw)+" {}", "admin"), 400)
}

func TestAITriggersLimitsAndClear(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.plugin.settings.AI = true
	f.plugin.settings.Model = "test"
	f.plugin.settings.Endpoint = f.server.URL
	f.plugin.settings.Turns = 2
	f.plugin.settings.Rate = 2
	if err := f.plugin.checkBot(ctx); err != nil {
		t.Fatal(err)
	}
	message := &Message{ID: 15, Text: "普通群聊", From: TGUser{ID: 77}, Date: f.clock.Unix()}
	message.Chat.ID = -100123456
	if err := f.plugin.handleMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	if f.calls["completions"] != 0 {
		t.Fatal("untriggered chat sent to model")
	}
	message.Text = "小路，测试"
	for count := 0; count < 3; count++ {
		if err := f.plugin.handleMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls["completions"] != 2 {
		t.Fatal("rate limit failed")
	}
	usage, err := f.plugin.usage(ctx)
	if err != nil || usage.Tokens != 84 || usage.Calls != 2 {
		t.Fatal("usage", usage, err)
	}
	f.clock = f.clock.Add(time.Minute)
	if err := f.plugin.handleMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	var history []ChatTurn
	_, _ = f.plugin.load(ctx, f.plugin.scoped("history/77"), &history)
	if len(history) != 4 {
		t.Fatal("history not bounded")
	}
	f.plugin.settings.Budget = 1000
	if err := f.plugin.handleMessage(ctx, message); err == nil {
		t.Fatal("budget exceeded")
	}
	mustStatus(t, f.request("POST", "/admin/clear", "", "admin"), 200)
	found, err := f.plugin.load(ctx, f.plugin.scoped("history/77"), &history)
	if err != nil || found {
		t.Fatal("context not removed")
	}
	f.plugin.settings.AI = false
	if err := f.plugin.handleMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	if f.calls["completions"] != 3 {
		t.Fatal("disabled AI called model")
	}
}

func TestChannelPNGFailureAndCooldown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.plugin.settings.Channels = true
	state, err := f.plugin.refreshChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeImage(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, f.request("GET", "/image", "", "user"), 200)
	mustStatus(t, f.request("GET", "/image", "", "anonymous"), 401)
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := f.plugin.broadcast(ctx, true); err == nil {
		t.Fatal("cooldown missing")
	}
	f.clock = f.clock.Add(time.Minute)
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal(err)
	}
	if f.calls["sendPhoto"] != 1 || f.calls["editMessageMedia"] != 1 {
		t.Fatal("must update original photo")
	}
	f.catalogError = true
	failed, err := f.plugin.refreshChannels(ctx)
	if err == nil || failed.Updated != f.clock.Unix() || failed.Encoded == "" {
		t.Fatal("failure lost last good data")
	}
	f.catalogError = false
	f.catalog = []byte(`{"code":0,"data":[]}`)
	empty, err := f.plugin.refreshChannels(ctx)
	if err != nil || len(empty.Groups) != 0 {
		t.Fatal("empty data", err)
	}
	f.plugin.settings.Channels = false
	if err := f.plugin.broadcast(ctx, true); err == nil {
		t.Fatal("disabled broadcaster")
	}
	response := f.request("GET", "/admin/status", "", "admin")
	mustStatus(t, response, 200)
	if strings.Contains(response.Body.String(), f.host.Env.DataDir) || strings.Contains(response.Body.String(), "fake-model-key") || strings.Contains(response.Body.String(), "test_secret") {
		t.Fatal("secret or absolute path leaked")
	}
}

func TestConcurrentClaims(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	var group sync.WaitGroup
	results := make(chan int, 8)
	for count := 0; count < 8; count++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response := f.host.Request(f.plugin.Handler(), httptest.NewRequest("POST", "/invite", nil), sdk.Caller{UserID: 9, Role: "user"})
			results <- response.Code
		}()
	}
	group.Wait()
	close(results)
	success := false
	for code := range results {
		if code == 200 {
			success = true
		} else if code != 409 {
			t.Fatal("unexpected concurrent status", code)
		}
	}
	if !success || f.calls["createChatInviteLink"] != 1 {
		t.Fatal("concurrent claims created duplicate invitations")
	}
}

func TestShutdownCancelsPollingAndClosesDatabase(t *testing.T) {
	host := plugintest.New(t)
	host.Env.Config, _ = sdk.NewConfig([]byte(`{"bot_token":"12345:test_secret_123456","chat_id":"-100123456"}`))
	entered := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		once.Do(func() { close(entered) })
		select {
		case <-request.Context().Done():
		case <-released:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(released) })
	plugin := New()
	plugin.telegramURL = server.URL
	if err := plugin.Init(context.Background(), host.Env); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plugin.Shutdown(context.Background()) })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("background poll did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := plugin.Shutdown(ctx); err != nil {
		t.Fatal("shutdown failed to cancel request", err)
	}
	if plugin.db != nil {
		t.Fatal("database left open")
	}
	if err := os.Rename(filepath.Join(host.Env.DataDir, "plugin.sqlite"), filepath.Join(host.Env.DataDir, "closed.sqlite")); err != nil {
		t.Fatal("database file handle still in use", err)
	}
}

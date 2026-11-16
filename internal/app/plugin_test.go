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
	"net/url"
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

// plugin_test.go exercises plugin persistence, HTTP permissions, and external
// effects using isolated SQLite directories and local API fixtures. Invitation
// scenarios route serialized Telegram updates through polling and verify actual
// approved users, durable consumption, and recovery after process restart.
//
// The Telegram fixture records admissions separately from returned responses so
// a connection loss can represent an operation that succeeded remotely. Restart
// scenarios reopen the same database and resume revocation, which checks that
// link consumption survives both an unavailable API and a fresh plugin process.
// Model and catalog fixtures exercise unrelated plugin features locally; these
// tests make no requests to a real Telegram group or model endpoint.

// fixture connects one plugin to isolated storage and controllable remote APIs.
type fixture struct {
	t               *testing.T
	host            *plugintest.Harness
	plugin          *Plugin
	server          *httptest.Server
	clock           time.Time
	linux           bool
	orders          []*pluginpb.Order
	catalog         []byte
	catalogError    bool
	updates         []Update
	calls           map[string]int
	fail            string
	failStatus      int      // injected failure status
	failDescription string   // injected refusal description
	operations      []string // ordered Telegram methods
	deleted         []int64  // deleted photo identifiers
	// failCall selects one invocation of fail; zero applies the injected failure
	// to every invocation so existing persistent-outage scenarios keep their scope.
	failCall         int              // failing method invocation number
	loseApproval     bool             // drop successful approval response
	admitted         []int64          // users admitted by fixture
	messages         []map[string]any // captured outgoing bot messages
	menus            []map[string]any // captured command menu requests
	approvalFinished chan struct{}    // dropped response handler completion
	modelBody        map[string]any
	groupType        string
	groupName        string
	joinByRequest    bool
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

// remote serves local Telegram and model responses and records external effects.
// Tests serialize calls through the plugin and configure failures before sending
// requests, including successful approvals whose responses are lost in transit.
func (f *fixture) remote(writer http.ResponseWriter, request *http.Request) {
	method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
	f.calls[method]++
	f.operations = append(f.operations, method)
	if method == f.fail && (f.failCall == 0 || f.calls[method] == f.failCall) {
		status := f.failStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}
		sdk.WriteJSON(writer, status, map[string]any{"ok": false, "error_code": status, "description": f.failDescription})
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
	case "setMyCommands":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		f.menus = append(f.menus, input)
	case "createChatInviteLink":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if input["expire_date"] != nil {
			f.t.Error("group invitation must omit expiry")
		}
		if input["member_limit"] != nil || input["creates_join_request"] != true {
			f.t.Error("invitation must require approval and omit member_limit")
		}
		result = map[string]any{"invite_link": fmt.Sprintf("https://t.me/+test%d", f.calls[method]), "creates_join_request": true}
	case "getUpdates":
		result = f.updates
	case "approveChatJoinRequest":
		var input struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		f.admitted = append(f.admitted, input.UserID)
		if f.loseApproval {
			// Signal handler completion independently of the lost network response so
			// tests can synchronize fixture mutations before issuing the next request.
			defer close(f.approvalFinished)
			// Commit the admission before closing the connection. The client sees
			// a network failure even though the first requester already entered.
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				f.t.Error(err)
				return
			}
			if err := connection.Close(); err != nil {
				f.t.Error(err)
			}
			return
		}
	case "declineChatJoinRequest":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if input["user_id"] == nil {
			f.t.Error("decline requires user_id")
		}
	case "sendMessage":
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		f.messages = append(f.messages, input)
	case "deleteMessage":
		var input struct {
			Chat string `json:"chat_id"`    // target group identifier
			ID   int64  `json:"message_id"` // superseded photo identifier
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if input.Chat != f.plugin.chat || input.ID <= 0 {
			f.t.Error("invalid deletion identity")
		}
		f.deleted = append(f.deleted, input.ID)
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
		// Distinct photo identifiers expose accidental reuse or deletion of the replacement.
		result = map[string]int64{"message_id": 455 + int64(f.calls["sendPhoto"])}
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
	invite := Invite{UserID: 9, URL: "https://t.me/+persist", Expires: first.clock.Add(time.Hour).Unix()}
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

// TestInvitationsAndPermissions claims a permanent bot link through the website.
// Its payload survives time and restart, and website authorization still rejects
// anonymous callers and accounts without either eligibility source.
func TestInvitationsAndPermissions(t *testing.T) {
	f := setup(t)
	f.plugin.settings.Invites = true
	if err := f.plugin.save(context.Background(), "settings", f.plugin.settings); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, f.request("GET", "/status", "", "anonymous"), 401)
	mustStatus(t, f.request("GET", "/admin/status", "", "user"), 403)
	mustStatus(t, f.request("POST", "/admin/clear", "", "user"), 403)
	response := f.request("POST", "/invite", "", "user")
	mustStatus(t, response, 200)
	var first struct {
		Invite Invite `json:"invite"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	entry, err := url.Parse(first.Invite.URL)
	if err != nil || entry.Path != "/TestBot" || entry.Query().Get("start") == "" || first.Invite.Expires != 0 || first.Invite.StartToken != "" {
		t.Fatal("website returned an invalid permanent bot link")
	}
	if f.calls["createChatInviteLink"] != 0 {
		t.Fatal("website created a group invitation before Start")
	}
	f.clock = f.clock.Add(365 * 24 * time.Hour)
	f.restart()
	response = f.request("POST", "/invite", "", "user")
	mustStatus(t, response, 200)
	var later struct {
		Invite Invite `json:"invite"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &later); err != nil {
		t.Fatal(err)
	}
	if later.Invite.URL != first.Invite.URL || later.Invite.Expires != 0 {
		t.Fatal("bot link expired or changed after restart")
	}
	f.linux = false
	f.orders = nil
	mustStatus(t, f.request("GET", "/image", "", "user"), 403)
	mustStatus(t, f.request("POST", "/invite", "", "user"), 403)
	f.orders = []*pluginpb.Order{{Type: "subscription", Status: "COMPLETED", PaidUnix: 1, Amount: "100"}, {Type: "balance", Status: "COMPLETED", PaidUnix: 0, Amount: "1"}, {Type: "balance", Status: "COMPLETED", PaidUnix: 1, Amount: "0"}}
	mustStatus(t, f.request("POST", "/invite", "", "user"), 403)
	f.orders = append(f.orders, &pluginpb.Order{Type: "balance", Status: "COMPLETED", PaidUnix: 1, Amount: "0.00000001"})
	mustStatus(t, f.request("POST", "/invite", "", "user"), 200)
	f.host.Env.Permissions = nil
	mustStatus(t, f.request("GET", "/status", "", "user"), 502)
}

// bindAccount installs a test binding for admission failure scenarios.
// Production redemption is covered separately through serialized private Start
// updates; these scenarios prepare ownership before injecting approval API failures.
func (f *fixture) bindAccount(telegramID int64) Invite {
	f.t.Helper()
	f.plugin.settings.Invites = true
	current, err := f.plugin.issueBotLink(context.Background(), 9, "test eligibility")
	if err != nil {
		f.t.Fatal(err)
	}
	current.TelegramID = telegramID
	current.BoundAt = f.clock.Unix()
	if err := f.plugin.save(context.Background(), f.plugin.inviteKey(9), current); err != nil {
		f.t.Fatal(err)
	}
	return current
}

// TestPublicGroupAndJoinRequests processes two requesters using one public-group
// invitation in the same poll. The bound owner is admitted without invitation
// attribution; unbound requesters remain rejected through cleanup failure and restart.
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
	f.bindAccount(777)
	if _, err := f.plugin.issue(context.Background(), 9, "test eligibility"); err != nil {
		t.Fatal(err)
	}
	var current Invite
	if _, err := f.plugin.load(context.Background(), f.plugin.inviteKey(9), &current); err != nil {
		t.Fatal(err)
	}
	raw := `{"update_id":300,"chat_join_request":{"chat":{"id":-100123456},"from":{"id":777}}}`
	var update Update
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	var second Update
	if err := json.Unmarshal([]byte(strings.ReplaceAll(raw, `"id":777`, `"id":778`)), &second); err != nil {
		t.Fatal(err)
	}
	second.ID = 301
	// Keep the Telegram link active after the first approval. Durable consumption
	// must still reject another requester whose update is already queued locally.
	f.fail = "revokeChatInviteLink"
	f.updates = []Update{update, second}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls["approveChatJoinRequest"] != 1 || len(f.admitted) != 1 || f.admitted[0] != 777 || f.calls["declineChatJoinRequest"] != 1 {
		t.Fatalf("one link admitted multiple users: %v", f.admitted)
	}
	var after Invite
	if _, err := f.plugin.load(context.Background(), f.plugin.inviteKey(9), &after); err != nil {
		t.Fatal(err)
	}
	if !after.Used || !after.Revoke || after.Revoked {
		t.Fatalf("admission must remain consumed while revocation is pending: %+v", after)
	}
	f.fail = ""
	f.restart()
	second.ID = 302
	f.updates = []Update{second}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.admitted) != 1 || f.calls["declineChatJoinRequest"] != 2 {
		t.Fatal("restart released an already consumed invitation")
	}
	direct := Update{}
	if err := json.Unmarshal([]byte(`{"update_id":303,"chat_join_request":{"chat":{"id":-100123456},"from":{"id":888},"invite_link":{"invite_link":"https://t.me/+other"}}}`), &direct); err != nil {
		t.Fatal(err)
	}
	f.updates = []Update{direct}
	if err := f.plugin.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls["declineChatJoinRequest"] != 3 {
		t.Fatal("非插件链接的入群请求未拒绝")
	}
	f.groupType = "group"
	mustStatus(t, f.request("POST", "/admin/check", "", "admin"), 502)
}

// TestApprovalOutcomes protects the consumed claim when Telegram's result is
// uncertain. A definite rejection permits retry, while lost responses and server
// failures cannot admit another person after reopening the database.
func TestApprovalOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		status       int
		lostResponse bool
		consumed     bool
	}{
		{name: "definite rejection", status: 400},
		{name: "server failure", status: 500, consumed: true},
		{name: "response lost after admission", lostResponse: true, consumed: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			f.bindAccount(777)
			_, err := f.plugin.issue(ctx, 9, "test eligibility")
			if err != nil {
				t.Fatal(err)
			}
			if scenario.status != 0 {
				f.fail = "approveChatJoinRequest"
				f.failStatus = scenario.status
			}
			f.loseApproval = scenario.lostResponse
			if scenario.lostResponse {
				f.approvalFinished = make(chan struct{})
			}
			if err := f.plugin.resolveJoin(ctx, 777); err == nil {
				t.Fatal("approval failure was not reported")
			}
			if scenario.lostResponse {
				// Network EOF can arrive before the handler returns. Wait for its
				// completion signal before reading admissions or changing remote failure controls.
				select {
				case <-f.approvalFinished:
				case <-time.After(2 * time.Second):
					t.Fatal("lost-response handler did not finish")
				}
			}
			var saved Invite
			if _, err := f.plugin.load(ctx, f.plugin.inviteKey(9), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Used != scenario.consumed || saved.Revoke != scenario.consumed {
				t.Fatalf("unexpected admission claim: %+v", saved)
			}
			f.fail = ""
			f.loseApproval = false
			f.restart()
			if err := f.plugin.resolveJoin(ctx, 777); err != nil {
				t.Fatal(err)
			}
			if len(f.admitted) > 1 {
				t.Fatalf("one invitation admitted multiple people: %v", f.admitted)
			}
			if scenario.consumed {
				if f.calls["approveChatJoinRequest"] != 1 || f.calls["declineChatJoinRequest"] != 1 {
					t.Fatal("uncertain approval released the invitation")
				}
			} else if len(f.admitted) != 1 || f.admitted[0] != 777 {
				t.Fatal("definite rejection did not permit a later admission")
			}
		})
	}
}

// TestLegacyInvitationReplacement loads an ordinary link saved by an earlier
// version. It must be revoked before replacement, and subsequent claims reuse
// the permanent approval invitation until it is used or explicitly revoked.
func TestLegacyInvitationReplacement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.plugin.settings.Invites = true
	legacy := f.bindAccount(777)
	legacy.URL = "https://t.me/+legacy"
	legacy.Expires = f.clock.Add(time.Hour).Unix()
	if err := f.plugin.save(ctx, f.plugin.inviteKey(9), legacy); err != nil {
		t.Fatal(err)
	}
	response := f.request("GET", "/status", "", "user")
	mustStatus(t, response, 200)
	var status struct {
		Invite Invite `json:"invite"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(status.Invite.URL, "https://t.me/TestBot?start=") {
		t.Fatal("status did not expose the permanent bot entry")
	}
	f.fail = "revokeChatInviteLink"
	if _, err := f.plugin.issue(ctx, 9, "test eligibility"); err == nil {
		t.Fatal("legacy replacement ignored revocation failure")
	}
	if f.calls["createChatInviteLink"] != 0 {
		t.Fatal("replacement was issued before the ordinary link was revoked")
	}
	f.fail = ""
	if err := f.plugin.retryRevokes(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := f.plugin.issue(ctx, 9, "test eligibility")
	if err != nil || !current.JoinRequest || current.URL == legacy.URL {
		t.Fatal("legacy link was not replaced with an approval invitation", err)
	}
	if _, err := f.plugin.issue(ctx, 9, "test eligibility"); err != nil {
		t.Fatal(err)
	}
	if f.calls["createChatInviteLink"] != 1 {
		t.Fatal("active approval invitation was not reused")
	}
}

// TestJoinRequestDiagnostics rejects unbound, revoked, and ineligible requesters.
// A group link alone cannot establish ownership, and qualifying callers require
// a completed private Start binding before admission is considered.
func TestJoinRequestDiagnostics(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		bound      bool
		revoked    bool
		ineligible bool
		detail     string
	}{
		{name: "unbound", detail: "尚未完成绑定"},
		{name: "revoked entry", bound: true, revoked: true, detail: "尚未完成绑定"},
		{name: "ineligible owner", bound: true, ineligible: true, detail: "暂不可用"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if scenario.bound {
				current := f.bindAccount(777)
				if _, err := f.plugin.issue(ctx, 9, "test eligibility"); err != nil {
					t.Fatal(err)
				}
				if scenario.revoked {
					current.StartToken = ""
					if err := f.plugin.save(ctx, f.plugin.inviteKey(9), current); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario.ineligible {
				f.linux = false
				f.orders = nil
			}
			if err := f.plugin.resolveJoin(ctx, 777); err != nil {
				t.Fatal(err)
			}
			var events []Event
			if _, err := f.plugin.load(ctx, f.plugin.scoped("events"), &events); err != nil {
				t.Fatal(err)
			}
			if f.calls["approveChatJoinRequest"] != 0 || f.calls["declineChatJoinRequest"] != 1 || len(events) == 0 || !strings.Contains(events[0].Detail, scenario.detail) {
				t.Fatalf("unexpected rejection: %+v", events)
			}
		})
	}
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
	f.bindAccount(777)
	f.fail = "createChatInviteLink"
	if _, err := f.plugin.issue(context.Background(), 9, "test eligibility"); err == nil {
		t.Fatal("uncertain creation accepted")
	}
	f.fail = ""
	if _, err := f.plugin.issue(context.Background(), 9, "test eligibility"); err == nil {
		t.Fatal("uncertain creation was retried")
	}
	if f.calls["createChatInviteLink"] != 1 {
		t.Fatal("uncertain creation duplicated")
	}
	f.clock = f.clock.Add(invitationRetryDelay + time.Second)
	if _, err := f.plugin.issue(context.Background(), 9, "test eligibility"); err != nil {
		t.Fatal(err)
	}
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

// TestChannelPNGFailureAndCooldown checks bounded PNG previews and group cooldowns.
// Successful refreshes replace the previous photo, while a catalog failure keeps
// the last successful data available for authenticated website previews.
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
	if f.calls["sendPhoto"] != 2 || f.calls["editMessageMedia"] != 0 || len(f.deleted) != 1 || f.deleted[0] != 456 {
		t.Fatal("must send a new photo and delete the previous one")
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
	if !success || f.calls["createChatInviteLink"] != 0 {
		t.Fatal("website claim created a group invitation before Start")
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

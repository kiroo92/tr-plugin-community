package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
)

// TestBindingAPI authenticates an administrator integration independently of website
// sessions. Successful queries return exactly yes or no, while missing keys,
// malformed IDs, duplicate headers, and storage failures retain distinct error statuses.
func TestBindingAPI(t *testing.T) {
	f := setup(t)
	f.bindAccount(777)
	const key = "test-binding-api-key-12345678901234567890"
	request := func(query, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/binding"+query, nil)
		if credential != "" {
			r.Header.Set("X-Plugin-Admin-Key", credential)
		}
		writer := httptest.NewRecorder()
		f.plugin.Handler().ServeHTTP(writer, r)
		return writer
	}
	if got := request("?user_id=9", key); got.Code != 503 {
		t.Fatal("unconfigured key accepted", got.Code)
	}
	config, err := sdk.NewConfig([]byte(`{"binding_api_key":"` + key + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.plugin.env.Config = config
	for _, credential := range []string{"", "wrong", key + "extra"} {
		if got := request("?user_id=9", credential); got.Code != 401 || strings.Contains(got.Body.String(), key) {
			t.Fatal("independent authentication failed", got.Code)
		}
	}
	for _, query := range []string{"", "?user_id=0", "?user_id=-1", "?user_id=abc", "?user_id=9223372036854775808", "?user_id=9&user_id=10"} {
		if got := request(query, key); got.Code != 400 {
			t.Fatal("invalid query accepted", query, got.Code)
		}
	}
	for _, item := range []struct {
		query string // positive website identity
		want  string // exact plain response
	}{{"?user_id=9", "是"}, {"?user_id=10", "否"}} {
		got := request(item.query, key)
		if got.Code != 200 || got.Body.String() != item.want || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("wrong binding response", got.Code, got.Body.String())
		}
	}
	f.plugin.mu.Lock()
	busy := request("?user_id=9", key)
	unauthorized := request("?user_id=9", "wrong")
	f.plugin.mu.Unlock()
	if busy.Code != 409 || unauthorized.Code != 401 {
		t.Fatal("busy state changed authentication or appeared as an absent binding")
	}
	r := httptest.NewRequest("GET", "/binding?user_id=9", nil)
	r.Header.Add("X-Plugin-Admin-Key", key)
	r.Header.Add("X-Plugin-Admin-Key", key)
	w := httptest.NewRecorder()
	f.plugin.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambiguous credential header accepted")
	}
	current, _, err := f.plugin.inviteForTelegram(context.Background(), 777)
	if err != nil {
		t.Fatal(err)
	}
	current.StartToken = ""
	current.Used = true
	if err := f.plugin.save(context.Background(), f.plugin.inviteKey(9), current); err != nil {
		t.Fatal(err)
	}
	if got := request("?user_id=9", key); got.Body.String() != "是" {
		t.Fatal("revoked entry or consumed admission hid a retained binding")
	}
	f.plugin.scope = "different-bot-group"
	if got := request("?user_id=9", key); got.Body.String() != "否" {
		t.Fatal("binding leaked across bot and group scope")
	}
	if err := f.plugin.save(context.Background(), f.plugin.inviteKey(9), "corrupted record"); err != nil {
		t.Fatal(err)
	}
	if got := request("?user_id=9", key); got.Code != 500 || got.Body.String() == "否" {
		t.Fatal("storage failure appeared as an unbound account")
	}
}

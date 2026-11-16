package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
)

// TestPoolCounts joins the complete exported group statistics by numeric identity.
// It protects measured zero, absent groups, stale timestamps, duplicate identities,
// invalid quantities, transport failures, and clearing observations after failure.
func TestPoolCounts(t *testing.T) {
	f := setup(t)
	body := fmt.Sprintf(`{"snapshot_at":%q,"stale":false,"group_stats":[{"id":12,"available_accounts":170},{"id":11,"available_accounts":563},{"id":13,"available_accounts":0}]}`, f.clock.Format(time.RFC3339))
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) }))
	t.Cleanup(server.Close)
	config, err := sdk.NewConfig([]byte(fmt.Sprintf(`{"pool_stats_url":%q}`, server.URL)))
	if err != nil {
		t.Fatal(err)
	}
	f.plugin.env.Config = config
	state := ChannelState{Updated: f.clock.Unix(), Groups: []Group{{ID: 11, Name: "【GPT】满血高缓不降智⭐️⭐️⭐️"}, {ID: 12, Name: "【GPT】特惠不降智⭐️"}, {ID: 13, Name: "Empty"}, {ID: 14, Name: "Unmapped"}}}
	f.plugin.loadPoolCounts(context.Background(), &state)
	if state.PoolError != "" || state.PoolAt == "" || state.PoolStale || state.Groups[0].Available == nil || *state.Groups[0].Available != 563 || state.Groups[1].Available == nil || *state.Groups[1].Available != 170 || state.Groups[2].Available == nil || *state.Groups[2].Available != 0 || state.Groups[3].Available != nil {
		t.Fatalf("incorrect group association or zero handling: %+v", state)
	}
	caption := channelCaption(state)
	if strings.Contains(caption, "[Empty]") || strings.Contains(caption, "[Unmapped]") {
		t.Fatal("other pools appeared in caption", caption)
	}
	for _, want := range []string{"[GPT满血] 可用账号：563", "[GPT特惠] 可用账号：170"} {
		if !strings.Contains(caption, want) {
			t.Fatal("caption missing available account summary", caption)
		}
	}
	body = fmt.Sprintf(`{"snapshot_at":%q,"group_stats":[{"id":11,"available_accounts":500}]}`, f.clock.Add(-time.Hour).Format(time.RFC3339))
	f.plugin.loadPoolCounts(context.Background(), &state)
	if !state.PoolStale || !strings.Contains(channelCaption(state), "旧快照") {
		t.Fatal("old snapshot appeared current")
	}
	for _, invalid := range []string{
		`{"group_stats":[]}`,
		`{"snapshot_at":"invalid","group_stats":[]}`,
		fmt.Sprintf(`{"snapshot_at":%q,"group_stats":[{"id":11,"available_accounts":-1}]}`, f.clock.Format(time.RFC3339)),
		fmt.Sprintf(`{"snapshot_at":%q,"group_stats":[{"id":11,"available_accounts":1},{"id":11,"available_accounts":2}]}`, f.clock.Format(time.RFC3339)),
		fmt.Sprintf(`{"snapshot_at":%q,"group_stats":[{"id":11}]}`, f.clock.Format(time.RFC3339)),
	} {
		body = invalid
		f.plugin.loadPoolCounts(context.Background(), &state)
		if state.PoolError == "" || state.PoolAt != "" {
			t.Fatal("invalid export accepted", invalid)
		}
		for _, group := range state.Groups {
			if group.Available != nil {
				t.Fatal("failed export retained an old count")
			}
		}
	}
	status = 503
	f.plugin.loadPoolCounts(context.Background(), &state)
	if state.PoolError == "" {
		t.Fatal("pool failure was hidden")
	}
	for index := range 12 {
		state.Groups = append(state.Groups, Group{ID: int64(30 + index), Name: strings.Repeat("😀", 40)})
	}
	state.Groups = state.Groups[:12]
	if len(utf16.Encode([]rune(channelCaption(state)))) > 1024 {
		t.Fatal("caption exceeds Telegram limit")
	}
}

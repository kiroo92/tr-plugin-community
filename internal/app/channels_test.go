package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// channels_test.go verifies Telegram photo replacement and durable cleanup through
// the local API fixture. The tests observe request order and saved message
// identities, including delivery failure, deletion failure, and recovery after a
// process restart while scheduled broadcasting is paused.

// TestBroadcastReplacementSafety replaces a confirmed photo after a failed send.
// The previous photo remains available until another send succeeds, and only
// that superseded identifier is deleted after the replacement has been persisted.
func TestBroadcastReplacementSafety(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.plugin.settings.Channels = true
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(time.Minute)
	f.fail = "sendPhoto"
	if err := f.plugin.broadcast(ctx, true); err == nil {
		t.Fatal("failed replacement was accepted")
	}
	var state ChannelState
	if _, err := f.plugin.load(ctx, f.plugin.scoped("channel"), &state); err != nil || state.Message != 456 || len(state.Cleanup) != 0 || f.calls["deleteMessage"] != 0 {
		t.Fatal("failed send removed the previous photo", err)
	}
	f.fail = ""
	f.clock = f.clock.Add(time.Minute)
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plugin.load(ctx, f.plugin.scoped("channel"), &state); err != nil || state.Message != 458 || len(state.Cleanup) != 0 {
		t.Fatal("replacement state was not saved", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 456 || f.calls["editMessageMedia"] != 0 {
		t.Fatal("wrong photo removed or previous photo edited", f.deleted)
	}
	last := len(f.operations)
	if last < 2 || f.operations[last-2] != "sendPhoto" || f.operations[last-1] != "deleteMessage" {
		t.Fatal("old photo deletion did not follow the new send", f.operations)
	}
}

// TestChannelCleanupRecovery restarts after a successful replacement whose old
// photo could not be deleted. Polling resumes cleanup after its persisted retry
// interval, including when channel broadcasts have been disabled across restart.
func TestChannelCleanupRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.plugin.settings.Channels = true
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(time.Minute)
	f.fail = "deleteMessage"
	if err := f.plugin.broadcast(ctx, true); err != nil {
		t.Fatal("cleanup failure hid successful replacement", err)
	}
	var state ChannelState
	if _, err := f.plugin.load(ctx, f.plugin.scoped("channel"), &state); err != nil || state.Message != 457 || len(state.Cleanup) != 1 || state.Cleanup[0] != 456 {
		t.Fatal("failed cleanup was not retained", err)
	}
	if err := f.plugin.retryChannelCleanup(ctx); err != nil || f.calls["deleteMessage"] != 1 {
		t.Fatal("cleanup retry bypassed cooldown", err)
	}
	f.fail = ""
	f.restart()
	f.clock = f.clock.Add(time.Minute)
	if err := f.plugin.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plugin.load(ctx, f.plugin.scoped("channel"), &state); err != nil || len(state.Cleanup) != 0 || state.Message != 457 {
		t.Fatal("cleanup did not recover independently of broadcasts", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 456 || f.calls["sendPhoto"] != 2 {
		t.Fatal("cleanup resent a photo or deleted its replacement", f.deleted)
	}
}

// TestChannelDeletionOutcomes distinguishes an already absent photo from other
// Telegram failures. Missing photos complete cleanup, while permission or server
// failures retain their queued identifier and never target the current photo.
func TestChannelDeletionOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		status      int    // injected HTTP status
		description string // validated refusal description
		complete    bool   // expected queue completion
	}{
		{400, "Bad Request: message to delete not found", true},
		{400, "Bad Request: message can't be deleted", false},
		{403, "Forbidden", false},
		{500, "server error", false},
	} {
		t.Run(scenario.description, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			state := ChannelState{Message: 457, Cleanup: []int64{457, 456}}
			if err := f.plugin.save(ctx, f.plugin.scoped("channel"), state); err != nil {
				t.Fatal(err)
			}
			f.fail, f.failStatus, f.failDescription = "deleteMessage", scenario.status, scenario.description
			err := f.plugin.retryChannelCleanup(ctx)
			if (err == nil) != scenario.complete {
				t.Fatal("incorrect deletion outcome", err)
			}
			if _, err := f.plugin.load(ctx, f.plugin.scoped("channel"), &state); err != nil {
				t.Fatal(err)
			}
			if state.Message != 457 || (len(state.Cleanup) == 0) != scenario.complete || f.calls["deleteMessage"] != 1 {
				t.Fatal("deletion outcome corrupted current identity or retry state", state)
			}
			if len(f.deleted) != 0 {
				t.Fatal("current message was sent for deletion")
			}
		})
	}
}

// TestPoolFailureDiagnostics reports an installed export's disabled switch directly.
// Unrecognized upstream bodies remain private, while status-specific diagnostics
// distinguish unavailable data from a missing route or inactive export setting.
func TestPoolFailureDiagnostics(t *testing.T) {
	got := poolResponseError(404, []byte(`{"message":"公开号池统计未启用"}`))
	if !strings.Contains(got, "开启「公开分组的可用账号统计」") {
		t.Fatal("disabled export was reported as invalid data", got)
	}
	for _, status := range []int{401, 403, 404, 429, 503} {
		got := poolResponseError(status, []byte(`{"message":"private-token-test"}`))
		if strings.Contains(got, "private-token-test") || got == "" {
			t.Fatal("upstream error was leaked or hidden", got)
		}
	}
}

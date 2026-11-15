package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// invites_test.go checks that group invitations remain valid across time and
// restart, including replacement of earlier expiring invitations. The shared
// Telegram fixture inspects creation parameters, while SQLite records establish
// whether the same invitation and bound account remain reusable.

// TestPermanentGroupInvitations creates a permanent invitation or replaces an
// expiring legacy link. Repeated issuance after a year and restart reuses the
// same URL, and failed revocation stays pending for cleanup.
func TestPermanentGroupInvitations(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new invitation"
		if legacy {
			name = "expiring legacy invitation"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			bound := f.bindAccount(777)
			if legacy {
				bound.URL = "https://t.me/+legacy-expiry"
				bound.Expires = f.clock.Add(time.Hour).Unix()
				bound.JoinRequest = true
				if err := f.plugin.save(ctx, f.plugin.inviteKey(9), bound); err != nil {
					t.Fatal(err)
				}
			}
			first, err := f.plugin.issue(ctx, 9, "test eligibility")
			if err != nil {
				t.Fatal(err)
			}
			if first.Expires != 0 || first.URL == "" || first.URL == bound.URL {
				t.Fatal("group invitation retained a time limit or legacy URL")
			}
			if legacy && f.calls["revokeChatInviteLink"] != 1 {
				t.Fatal("expiring invitation was not revoked before replacement")
			}
			var events []Event
			if _, err := f.plugin.load(ctx, f.plugin.scoped("events"), &events); err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 || strings.Contains(events[0].Detail, "有效期") || strings.Contains(events[0].Detail, "分钟") {
				t.Fatal("new invitation event reports an expiry")
			}
			f.clock = f.clock.Add(365 * 24 * time.Hour)
			f.restart()
			later, err := f.plugin.issue(ctx, 9, "test eligibility")
			if err != nil {
				t.Fatal(err)
			}
			if later.URL != first.URL || later.Expires != 0 || later.StartToken != bound.StartToken || later.TelegramID != 777 {
				t.Fatal("invitation or ownership changed after time and restart")
			}
			if f.calls["createChatInviteLink"] != 1 {
				t.Fatal("permanent invitation was recreated due to elapsed time")
			}
			f.fail = "revokeChatInviteLink"
			if err := f.plugin.revoke(ctx, &later); err == nil {
				t.Fatal("permanent invitation revocation error was treated as expiry")
			}
			if !later.Revoke || later.Revoked {
				t.Fatal("permanent invitation cleanup did not remain pending")
			}
		})
	}
}

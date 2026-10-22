package taskengine

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordingEngine(prefix []string) (Engine, *[][]string) {
	var calls [][]string
	return Engine{
		Tag: "main", Owner: "owner", Prefix: prefix, ClaimTTL: time.Hour,
		Run: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte(`{"token": 7, "expiresAt": "x", "revision": 3}`), nil
		},
	}, &calls
}

func TestEnginePrefixAndClaimTTL(t *testing.T) {
	e, calls := recordingEngine([]string{"tasks"})
	if _, err := e.Claim(context.Background(), 4, 2, "req"); err != nil {
		t.Fatal(err)
	}
	want := []string{"tasks", "claim", "-json", "-tag", "main", "-owner", "owner", "-revision", "2", "-request-id", "req", "-ttl", "3600", "4"}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Fatalf("claim argv = %v, want %v", (*calls)[0], want)
	}
}

func TestEngineSetStatusClaimedAndUnclaimed(t *testing.T) {
	e, calls := recordingEngine(nil)
	if err := e.SetStatus(context.Background(), 4, 0, 5, "done"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetStatus(context.Background(), 4, 9, 5, "in-progress"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join((*calls)[0], " "), "set-status -json -tag main -revision 5 4 done"; got != want {
		t.Fatalf("unclaimed argv = %q, want %q", got, want)
	}
	if got, want := strings.Join((*calls)[1], " "), "set-status -json -tag main -revision 5 -owner owner -token 9 4 in-progress"; got != want {
		t.Fatalf("claimed argv = %q, want %q", got, want)
	}
}

func TestEngineReleaseAndCancelCarryTheirTokens(t *testing.T) {
	e, calls := recordingEngine(nil)
	if err := e.Release(context.Background(), 4, 9, 6); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelRun(context.Background(), 2, 9, 1, 10, "req"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join((*calls)[0], " "), "release -json -tag main -owner owner -token 9 -revision 6 4"; got != want {
		t.Fatalf("release argv = %q, want %q", got, want)
	}
	if got, want := strings.Join((*calls)[1], " "), "run cancel -json -owner owner -claim-token 9 -attempt 1 -attempt-token 10 -request-id req 2"; got != want {
		t.Fatalf("cancel argv = %q, want %q", got, want)
	}
}

func TestConfirmRemovedWorktreeRequiresCanonicalReceipt(t *testing.T) {
	for _, fixture := range []struct {
		name, raw string
		valid     bool
	}{
		{"removed", `{"entries":[{"runId":9,"operation":"remove","status":"removed","path":"/family/wt","branch":"tasks/main/4"}]}`, true},
		{"pending", `{"entries":[{"runId":9,"operation":"remove","status":"pending","path":"/family/wt","branch":"tasks/main/4"}]}`, false},
		{"other run", `{"entries":[{"runId":10,"operation":"remove","status":"removed","path":"/family/wt","branch":"tasks/main/4"}]}`, false},
		{"still active", `{"entries":[{"runId":9,"operation":"remove","status":"removed","path":"/family/wt","branch":"tasks/main/4"},{"runId":9,"operation":"provision","status":"bound","active":true}]}`, false},
		{"no receipt", `{"entries":[]}`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			e := Engine{Run: func(_ context.Context, args ...string) ([]byte, error) {
				if strings.Join(args, " ") != "worktree list -json" {
					t.Fatalf("unexpected mutation: %v", args)
				}
				return []byte(fixture.raw), nil
			}}
			err := e.ConfirmRemovedWorktree(context.Background(), 9, "/family/wt", "tasks/main/4")
			if (err == nil) != fixture.valid {
				t.Fatalf("receipt validation: %v", err)
			}
		})
	}
}

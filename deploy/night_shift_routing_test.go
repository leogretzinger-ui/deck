package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The overnight capacity router lives in shell, so the compiler cannot see
// what it reads out of the harnesses payload. These tests drive the real
// select_target out of scripts/night-shift/night-shift.sh over fixture
// payloads shaped like the ones the capacity API serves, so a field the API
// stops serving — or one the script starts requiring — shows up as a failed
// routing decision rather than as a silent blanket refusal at 3am.

// nightShiftRunner sources the script and calls one function from it. Sourcing
// runs the script's dispatcher, so it is entered through report: that command
// only reads the ledger, and from an empty directory it finds none and
// returns, leaving every function defined. Routing flags go through the
// script's own parse_args, and the payload reaches select_target as an
// argument exactly as the drain loop passes it.
const nightShiftRunner = `set -euo pipefail
source "$1" report >/dev/null
parse_args --targets "$3" --ceiling "$4" --policy "$5"
select_target "$2"
`

// selectTarget returns the decision lines the real script emits for one
// payload.
func selectTarget(t *testing.T, payload, targets, ceiling, policy string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	script, err := filepath.Abs(filepath.Join("..", "scripts", "night-shift", "night-shift.sh"))
	if err != nil {
		t.Fatalf("resolve night-shift.sh: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("night-shift.sh is missing: %v", err)
	}
	// An empty working directory keeps the run from reading or writing any
	// ledger, and keeps the repository clean.
	dir := t.TempDir()
	runner := filepath.Join(dir, "run-select-target.sh")
	if err := os.WriteFile(runner, []byte(nightShiftRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script, payload, targets, ceiling, policy)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("select_target failed: %v\n%s", err, output)
	}
	return strings.Split(strings.TrimRight(string(output), "\n"), "\n")
}

func assertDecision(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("routing decision mismatch\n got: %q\nwant: %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routing decision mismatch at line %d\n got: %q\nwant: %q", i+1, got, want)
		}
	}
}

// profile renders one harness profile in the shape the capacity API serves.
// The fields are exactly the ones the handler marshals - id, configured, and
// normalized capacity evidence - because a fixture carrying a field the API
// does not serve would let a read of that field pass here and refuse every
// target in production. No launch-gate field appears, for the same reason:
// the API does not serve one.
func profile(id, evidence string) string {
	return fmt.Sprintf(`{"id":%q,"configured":true,"evidence":%s}`, id, evidence)
}

func payload(profiles ...string) string {
	return `{"profiles":[` + strings.Join(profiles, ",") + `]}`
}

func window(used int, resetsAt string) string {
	if resetsAt == "" {
		return fmt.Sprintf(`{"name":"five_hour","scope":"account","used_percent":%d,"resets_at":null}`, used)
	}
	return fmt.Sprintf(`{"name":"five_hour","scope":"account","used_percent":%d,"resets_at":%q}`, used, resetsAt)
}

func available(windows ...string) string {
	return `{"state":"available","windows":[` + strings.Join(windows, ",") + `]}`
}

// TestNightShiftRoutesWithoutALaunchGateField pins the regression: the
// harnesses payload carries capacity evidence and nothing else, and that
// payload must route. A required read of any field the API does not serve
// turns every target into a refusal for the whole night.
func TestNightShiftRoutesWithoutALaunchGateField(t *testing.T) {
	body := payload(profile("claude", available(window(12, ""))))
	if strings.Contains(body, `"available":`) {
		t.Fatal("the fixture must carry no launch-gate field; it is the payload the API serves")
	}
	got := selectTarget(t, body, "claude=fable", "80", "most-idle")
	assertDecision(t, got, []string{"PICK\tclaude\tfable\t12"})
}

func TestNightShiftRoutingDecisions(t *testing.T) {
	future := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Second)
	futureStamp := future.Format("2006-01-02T15:04:05Z")
	past := time.Now().UTC().Add(-90 * time.Minute).Format("2006-01-02T15:04:05Z")

	cases := []struct {
		name    string
		payload string
		targets string
		ceiling string
		policy  string
		want    []string
	}{
		{
			name:    "most idle target under the ceiling is picked",
			payload: payload(profile("claude", available(window(64, ""))), profile("codex", available(window(9, "")))),
			targets: "claude=fable,codex=gpt-5.6-sol",
			ceiling: "80",
			policy:  "most-idle",
			want:    []string{"PICK\tcodex\tgpt-5.6-sol\t9"},
		},
		{
			name:    "preference policy takes the first qualifying target in order",
			payload: payload(profile("claude", available(window(64, ""))), profile("codex", available(window(9, "")))),
			targets: "claude=fable,codex=gpt-5.6-sol",
			ceiling: "80",
			policy:  "preference",
			want:    []string{"PICK\tclaude\tfable\t64"},
		},
		{
			name:    "the busiest window decides, not the idlest",
			payload: payload(profile("claude", available(window(5, ""), window(88, "")))),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tbusiest window 88% >= ceiling 80%",
				"NONE",
			},
		},
		{
			name:    "a target with no profile is refused as not configured",
			payload: payload(profile("claude", available(window(10, "")))),
			targets: "codex=gpt-5.6-sol,claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tcodex\tnot configured",
				"PICK\tclaude\tfable\t10",
			},
		},
		{
			name:    "an exhausted provider is refused by its state",
			payload: payload(profile("claude", `{"state":"exhausted","windows":[`+window(100, futureStamp)+`]}`)),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tstate exhausted",
				"NONE",
			},
		},
		{
			name:    "an absent state is refused as unknown, never as headroom",
			payload: payload(profile("claude", `{"windows":[`+window(3, "")+`]}`)),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tstate unknown",
				"NONE",
			},
		},
		{
			name:    "a provider reporting no windows is refused",
			payload: payload(profile("claude", `{"state":"available","windows":[]}`)),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tno capacity windows",
				"NONE",
			},
		},
		{
			name:    "a window exactly at the ceiling is refused",
			payload: payload(profile("claude", available(window(80, "")))),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tbusiest window 80% >= ceiling 80%",
				"NONE",
			},
		},
		{
			name:    "an at-ceiling provider whose reset has passed can never qualify",
			payload: payload(profile("claude", available(window(96, past)))),
			targets: "claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tbusiest window 96% >= ceiling 80%",
				"NONE",
			},
		},
		{
			name: "sleep waits for an available provider's reset and ignores an exhausted one",
			payload: payload(
				profile("codex", `{"state":"exhausted","windows":[`+window(100, time.Now().UTC().Add(10*time.Minute).Format("2006-01-02T15:04:05Z"))+`]}`),
				profile("claude", available(window(96, futureStamp))),
			),
			targets: "codex=gpt-5.6-sol,claude=fable",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tcodex\tstate exhausted",
				"REFUSE\tclaude\tbusiest window 96% >= ceiling 80%",
				fmt.Sprintf("SLEEP\t%d", future.Unix()),
			},
		},
		{
			name:    "headroom elsewhere outranks a pending reset",
			payload: payload(profile("claude", available(window(96, futureStamp))), profile("codex", available(window(20, "")))),
			targets: "claude=fable,codex=gpt-5.6-sol",
			ceiling: "80",
			policy:  "most-idle",
			want: []string{
				"REFUSE\tclaude\tbusiest window 96% >= ceiling 80%",
				"PICK\tcodex\tgpt-5.6-sol\t20",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := selectTarget(t, testCase.payload, testCase.targets, testCase.ceiling, testCase.policy)
			assertDecision(t, got, testCase.want)
		})
	}
}

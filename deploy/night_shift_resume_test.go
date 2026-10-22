package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A drain that loses its process must not pay for the same work twice. The
// loop's applied list is memory; the ledger is the durable record, so these
// drive the real resumed_applied_tasks over ledgers a killed run could leave.
// The ledger fixture is put in place AFTER sourcing: sourcing enters through
// report, which reads any ledger it finds, so the directory has to look empty
// until the function under test is the one reading.
const nightShiftResumeRunner = `set -euo pipefail
source "$1" report >/dev/null
[ -f fixture.jsonl ] && cp fixture.jsonl night-shift-ledger.jsonl
resumed_applied_tasks
`

func resumedApplied(t *testing.T, ledger string) string {
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
	dir := t.TempDir()
	if ledger != "" {
		if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(ledger), 0o644); err != nil {
			t.Fatalf("write ledger fixture: %v", err)
		}
	}
	runner := filepath.Join(dir, "run-resume.sh")
	if err := os.WriteFile(runner, []byte(nightShiftResumeRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script)
	command.Dir = dir
	// Stdout only: the applied list is the value, and a recovery notice about
	// a torn tail belongs on stderr where it cannot be mistaken for one.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("resumed_applied_tasks: %v\n%s%s", err, out, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func resumeRefused(t *testing.T, ledger string) string {
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
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(ledger), 0o644); err != nil {
		t.Fatalf("write ledger fixture: %v", err)
	}
	runner := filepath.Join(dir, "run-resume.sh")
	if err := os.WriteFile(runner, []byte(nightShiftResumeRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script)
	command.Dir = dir
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("the reader accepted a ledger it cannot account for:\n%s", out)
	}
	return string(out)
}

// A receipt that cannot be read is a task the run may already have paid for.
// Only a row cut mid-write at the end is recoverable; anywhere else the run
// stops rather than forgetting it.
func TestNightShiftResumeStopsOnAnUnreadableReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ledger string
	}{
		{
			name:   "malformed between valid receipts",
			ledger: receipt("41", "applied", true) + "{\"task\":\"42\",\"completed\":tr}\n" + receipt("43", "applied", true),
		},
		{
			// Terminated, so it is not the torn tail a killed append leaves.
			name:   "malformed as a terminated final row",
			ledger: receipt("41", "applied", true) + "{\"task\":\"42\",\"completed\":tr}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := resumeRefused(t, tc.ledger); !strings.Contains(out, "not a readable receipt") {
				t.Errorf("stopped for the wrong reason:\n%s", out)
			}
		})
	}
}

func receipt(task, status string, completed bool) string {
	done := "false"
	if completed {
		done = "true"
	}
	return `{"task":"` + task + `","completed":` + done + `,"status":"` + status +
		`","provider":"claude","model":"fable","windows_before":{},"windows_after":{}}` + "\n"
}

func TestNightShiftResumesItsAppliedGuardFromTheLedger(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ledger string
		want   string
	}{
		{
			name:   "no ledger at all is a first run",
			ledger: "",
			want:   "",
		},
		{
			name:   "an applied receipt is remembered across the restart",
			ledger: receipt("41", "applied", true),
			want:   "41",
		},
		{
			// The whole point: these are the tasks the board may still offer,
			// and dispatching one again spends a second subscription on work
			// that is already done.
			name:   "only applied receipts count",
			ledger: receipt("41", "applied", true) + receipt("42", "refused", false) + receipt("43", "applied", false),
			want:   "41",
		},
		{
			name:   "a task dispatched twice is named once",
			ledger: receipt("41", "applied", true) + receipt("41", "applied", true),
			want:   "41",
		},
		{
			// A killed run leaves a half-written line. The rows before it are
			// still evidence, and discarding them would re-dispatch everything.
			name:   "a torn final line does not discard the rows before it",
			ledger: receipt("41", "applied", true) + `{"task":"42","completed":tr`,
			want:   "41",
		},
		{
			// The restart reads the ledger BEFORE the next append repairs any
			// boundary, so a receipt that lost only its newline has to count
			// here or its task is dispatched and paid for a second time.
			name:   "a complete final row missing its newline still counts",
			ledger: receipt("41", "applied", true) + strings.TrimSuffix(receipt("42", "applied", true), "\n"),
			want:   "41 42",
		},
		{
			name:   "order follows the ledger",
			ledger: receipt("7", "applied", true) + receipt("3", "applied", true),
			want:   "7 3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumedApplied(t, tc.ledger); got != tc.want {
				t.Errorf("resumed_applied_tasks = %q, want %q", got, tc.want)
			}
		})
	}
}

// The guard is the loop consulting the ledger, not the reader alone, so this
// drives the real drain loop with a stub `noise` offering a task a receipt
// already closed as applied. The loop must refuse it. The stub answers only
// `tasks next`, which is all the loop asks before this decision.
const nightShiftDrainStub = `#!/usr/bin/env bash
if [ "$1" = "tasks" ] && [ "$2" = "next" ]; then
  echo '{"id":"41","title":"already applied"}'
  exit 0
fi
echo "{}"
`

func TestNightShiftDrainRefusesATaskTheLedgerClosed(t *testing.T) {
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
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "noise"), []byte(nightShiftDrainStub), 0o755); err != nil {
		t.Fatalf("write noise stub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "night-shift-ledger.jsonl"), []byte(receipt("41", "applied", true)), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	command := exec.Command(bash, script, "drain", "--project-id", "p1")
	command.Dir = dir
	command.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NOISEDECK_EXPERIMENT_CAPABILITY=test",
	)
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("drain dispatched a task the ledger had already closed:\n%s", out)
	}
	if !strings.Contains(string(out), "refusing to dispatch it again") {
		t.Errorf("drain stopped for the wrong reason:\n%s", out)
	}
}

// A torn tail is only survivable if the NEXT receipt still lands as its own
// record. append_receipt writes straight to the file, so without a boundary
// the fragment and the next receipt fuse into one unreadable line and the
// work that receipt records is forgotten. This drives the real append.
const nightShiftAppendRunner = `set -euo pipefail
source "$1" report >/dev/null
cp fixture.jsonl night-shift-ledger.jsonl
# The ledger's identity either side of the append. Rewriting it in place keeps
# the same file and has a window where it is shorter than what was committed;
# a rename has no such window.
stat -c %i night-shift-ledger.jsonl > ledger-id.before
append_receipt "$2" "$3" "$4" claude fable corr-1 true true applied '{}' '{}'
stat -c %i night-shift-ledger.jsonl > ledger-id.after
resumed_applied_tasks
`

func TestNightShiftAppendSurvivesATornTail(t *testing.T) {
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
	dir := t.TempDir()
	torn := receipt("41", "applied", true) + `{"task":"42","completed":tr`
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(torn), 0o644); err != nil {
		t.Fatalf("write ledger fixture: %v", err)
	}
	runner := filepath.Join(dir, "run-append.sh")
	if err := os.WriteFile(runner, []byte(nightShiftAppendRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script, "t0", "t1", "43")
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("append then resume: %v\n%s%s", err, out, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != "41 43" {
		t.Errorf("after appending onto a torn tail the reader saw %q, want %q", got, "41 43")
	}
}

// The other half of the boundary: a receipt that was written whole and lost
// only its newline is a real record, so it is kept rather than quarantined.
func TestNightShiftAppendKeepsACompleteRowMissingItsNewline(t *testing.T) {
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
	dir := t.TempDir()
	unterminated := receipt("41", "applied", true) + strings.TrimSuffix(receipt("42", "applied", true), "\n")
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(unterminated), 0o644); err != nil {
		t.Fatalf("write ledger fixture: %v", err)
	}
	runner := filepath.Join(dir, "run-append.sh")
	if err := os.WriteFile(runner, []byte(nightShiftAppendRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script, "t0", "t1", "43")
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("append then resume: %v\n%s%s", err, out, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != "41 42 43" {
		t.Errorf("a complete row missing its newline was lost: saw %q, want %q", got, "41 42 43")
	}
	if _, err := os.Stat(filepath.Join(dir, "night-shift-ledger.jsonl.torn")); err == nil {
		t.Error("a complete receipt was quarantined")
	}
}

// The guard has to hold on the path a restart actually takes: the loop reads
// the ledger before the next append repairs any boundary, so a receipt that
// lost only its newline must stop a dispatch on its own.
func TestNightShiftDrainRefusesATaskWhoseReceiptLostItsNewline(t *testing.T) {
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
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "noise"), []byte(nightShiftDrainStub), 0o755); err != nil {
		t.Fatalf("write noise stub: %v", err)
	}
	unterminated := strings.TrimSuffix(receipt("41", "applied", true), "\n")
	if err := os.WriteFile(filepath.Join(dir, "night-shift-ledger.jsonl"), []byte(unterminated), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	command := exec.Command(bash, script, "drain", "--project-id", "p1")
	command.Dir = dir
	command.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NOISEDECK_EXPERIMENT_CAPABILITY=test",
	)
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("drain dispatched a task whose receipt lost only its newline:\n%s", out)
	}
	if !strings.Contains(string(out), "refusing to dispatch it again") {
		t.Errorf("drain stopped for the wrong reason:\n%s", out)
	}
}

// Recovery must never leave the ledger shorter than what was committed. The
// replacement is written beside it and renamed, so every observable state is
// either the original file or the repaired one.
func TestNightShiftRecoveryKeepsTheCommittedReceipts(t *testing.T) {
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
	dir := t.TempDir()
	torn := receipt("41", "applied", true) + receipt("42", "applied", true) + `{"task":"43","completed":tr`
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(torn), 0o644); err != nil {
		t.Fatalf("write ledger fixture: %v", err)
	}
	runner := filepath.Join(dir, "run-append.sh")
	if err := os.WriteFile(runner, []byte(nightShiftAppendRunner), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	command := exec.Command(bash, runner, script, "t0", "t1", "44")
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("append then resume: %v\n%s%s", err, out, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != "41 42 44" {
		t.Errorf("recovery lost a committed receipt: saw %q, want %q", got, "41 42 44")
	}
	quarantined, err := os.ReadFile(filepath.Join(dir, "night-shift-ledger.jsonl.torn"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if !strings.Contains(string(quarantined), `"task":"43"`) {
		t.Errorf("the fragment was not preserved: %q", quarantined)
	}
	if _, err := os.Stat(filepath.Join(dir, "night-shift-ledger.jsonl.rewrite")); err == nil {
		t.Error("recovery left its replacement file behind")
	}
	// A death while the ledger is being rewritten in place loses the receipts
	// already committed. A rename has no such window, and it shows: the ledger
	// is a different file afterwards, where truncating keeps the same one.
	idBefore, err := os.ReadFile(filepath.Join(dir, "ledger-id.before"))
	if err != nil {
		t.Fatalf("read ledger id: %v", err)
	}
	idAfter, err := os.ReadFile(filepath.Join(dir, "ledger-id.after"))
	if err != nil {
		t.Fatalf("read ledger id: %v", err)
	}
	if string(idBefore) == string(idAfter) {
		t.Error("recovery rewrote the ledger in place instead of replacing it")
	}
}

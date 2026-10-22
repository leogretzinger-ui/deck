package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestClient returns a client on a throwaway socket.
//
// Automated tests use temporary tmux namespaces so they can never reach an
// user's sessions. Every test gets its own socket directory, and the
// server is killed on cleanup.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	c, err := NewClient(Config{
		SocketPath: filepath.Join(dir, "ac.sock"),
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = c.KillServer(context.Background())
	})
	return c
}

// mustCreate creates a session and returns its pane id.
func mustCreate(t *testing.T, c *Client, s SessionSpec) *PaneInfo {
	t.Helper()
	info, err := c.CreateSession(context.Background(), s)
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", s.Name, err)
	}
	return info
}

func spec(name string, argv ...string) SessionSpec {
	return SessionSpec{
		Name:       name,
		WorkDir:    "/tmp",
		Argv:       argv,
		Generation: 1,
		Marker:     "marker-" + name,
	}
}

func TestNewClientRejectsLooseSocketDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := NewClient(Config{SocketPath: filepath.Join(dir, "ac.sock")})
	if err == nil {
		t.Fatal("NewClient accepted a group/world-accessible socket directory")
	}
	if !strings.Contains(err.Error(), "0700") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestNewClientRequiresSocketPath(t *testing.T) {
	if _, err := NewClient(Config{SocketPath: "  "}); err == nil {
		t.Fatal("NewClient accepted a blank socket path")
	}
}

// tmux must run the provider executable directly as the pane command. It
// must not start a shell and type the command into it, because input after
// the CLI exits would then fall through to that shell.
func TestCreateSessionRunsExecutableDirectlyWithoutAShell(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate(t, c, spec("direct", "/bin/sleep", "300"))

	info, err := c.PaneInfoForSession(ctx, "direct")
	if err != nil {
		t.Fatalf("PaneInfo: %v", err)
	}

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", info.PID))
	if err != nil {
		t.Fatalf("read cmdline: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	want := []string{"/bin/sleep", "300"}
	if len(got) != len(want) {
		t.Fatalf("pane command is %v, want exactly %v (a wrapper shell would add entries)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pane command[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if info.Executable != "" && !strings.HasSuffix(info.Executable, "sleep") {
		t.Errorf("executable = %q, want the sleep binary", info.Executable)
	}
}

// The behaviour this whole design depends on: a pane whose command exits
// immediately must survive as a dead pane, carrying its exit evidence.
//
// Measured on tmux 3.6a, setting remain-on-exit AFTER new-session loses the
// session 8 times out of 10 for an instantly-exiting command. This asserts the
// server-config approach does not.
func TestInstantlyExitingCommandLeavesADeadPane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const trials = 10
	reported := 0
	for i := 0; i < trials; i++ {
		name := fmt.Sprintf("quick%d", i)
		spawned := mustCreate(t, c, spec(name, "/bin/false"))
		exists, err := c.HasSession(ctx, name)
		if err != nil {
			t.Fatalf("trial %d: HasSession: %v", i, err)
		}
		if !exists {
			t.Fatalf("trial %d: the session vanished when its command exited; "+
				"exit evidence would be lost", i)
		}
		// A create can legitimately return while the command is still running:
		// the pane was observed alive and the exit lands just after. The
		// contract is that the pane BECOMES dead while its session survives,
		// not that it is already dead the instant create returns, so wait for
		// the transition instead of assuming it.
		// Read only tmux's flags until it confirms death: a live identity
		// query can race with /proc disappearing as the command exits.
		deadline := time.Now().Add(10 * time.Second)
		for {
			state, err := c.PaneInputState(ctx, spawned.PaneID)
			if err != nil {
				t.Fatalf("trial %d: PaneInputState: %v", i, err)
			}
			if state.Dead {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("trial %d: pane is not marked dead", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
		info, err := c.PaneInfoByID(ctx, spawned.PaneID)
		if err != nil {
			t.Fatalf("trial %d: PaneInfo: %v", i, err)
		}
		if !info.Dead {
			t.Errorf("trial %d: pane is not marked dead", i)
		}
		// A command this short can die before tmux has finished registering
		// the job behind its pane, and such a death is reaped with no cause
		// recorded — permanently. That is tmux's behaviour, not a contract
		// Deck can hold it to, so a reported status must be the RIGHT status
		// while a missing one is counted rather than failed.
		if info.ExitStatus != nil {
			reported++
			if *info.ExitStatus != 1 {
				t.Errorf("trial %d: exit status = %d, want 1", i, *info.ExitStatus)
			}
		}
	}
	// Losing every cause would mean exit evidence never survives at all,
	// which is the regression this test exists to catch.
	if reported < trials/2 {
		t.Errorf("only %d of %d trials recorded an exit status; exit evidence is not surviving",
			reported, trials)
	}
}

// The transition budget is for retrying an already-observed /proc race. A
// normal first observation retains the caller's deadline even when a loaded
// host makes the tmux round trip take longer than that retry budget.
func TestSpawnObservationDoesNotCapInitialRead(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	c := newTestClient(t)
	spawned := mustCreate(t, c, spec("slow-observation", "/bin/sleep", "300"))

	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "tmux")
	script := fmt.Sprintf("#!/bin/sh\nsleep 0.6\nexec %q \"$@\"\n", realTmux)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write tmux wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	started := time.Now()
	info, err := c.observeSpawnedPane(context.Background(), spawned.PaneID)
	if err != nil {
		t.Fatalf("observeSpawnedPane after a slow first read: %v", err)
	}
	if elapsed := time.Since(started); elapsed < spawnTransitionLimit {
		t.Fatalf("first observation returned after %v, want wrapper delay beyond %v", elapsed, spawnTransitionLimit)
	}
	if info.SessionID != spawned.SessionID || info.PaneID != spawned.PaneID || info.PID != spawned.PID {
		t.Fatalf("observed identity = (%s, %s, %d), want (%s, %s, %d)",
			info.SessionID, info.PaneID, info.PID,
			spawned.SessionID, spawned.PaneID, spawned.PID)
	}
}

// A dead pane reports an exit status or a signal, never both. The sessions
// table's exit-code-xor-signal constraint depends on that shape.
//
// tmux does not always publish a cause at all. A process that dies while tmux
// is still registering the job behind its pane is reaped with neither field
// set, and it stays that way — the evidence is lost, not late. Deck already
// treats a dead pane carrying no evidence as interrupted rather than inventing
// one, so what is worth pinning here is the SHAPE of a cause when tmux reports
// it. Each case repeats until tmux publishes one and fails only if none ever
// arrives, which is what a real regression in this contract would look like.
func TestDeadPaneReportsStatusOrSignalNotBoth(t *testing.T) {
	t.Run("normal exit", func(t *testing.T) {
		_, dead := untilCauseReported(t, func(c *Client) string {
			mustCreate(t, c, spec("exited", "/bin/false"))
			return "exited"
		})
		if dead.ExitStatus == nil {
			t.Fatal("a reported cause carried neither status nor signal")
		}
		if dead.ExitSignal != nil {
			t.Errorf("a normal exit also reported signal %d", *dead.ExitSignal)
		}
	})

	t.Run("signal death", func(t *testing.T) {
		_, dead := untilCauseReported(t, func(c *Client) string {
			created := mustCreate(t, c, spec("signalled", "/bin/sleep", "300"))
			info, err := c.PaneInfoAfterSpawn(context.Background(), created)
			if err != nil {
				t.Fatalf("PaneInfoAfterSpawn: %v", err)
			}
			proc, err := os.FindProcess(info.PID)
			if err != nil {
				t.Fatalf("find process: %v", err)
			}
			if err := proc.Signal(os.Interrupt); err != nil {
				t.Fatalf("signal: %v", err)
			}
			return "signalled"
		})
		if dead.ExitSignal == nil {
			t.Fatal("a reported cause carried neither status nor signal")
		}
		if dead.ExitStatus != nil {
			t.Errorf("a signalled death also reported exit status %d", *dead.ExitStatus)
		}
	})
}

// untilCauseReported runs a death scenario on a fresh server until tmux
// publishes a cause for it, and returns that observation. Each attempt gets
// its own server so a lost cause cannot contaminate the next one.
func untilCauseReported(t *testing.T, scenario func(*Client) string) (*Client, *PaneInfo) {
	t.Helper()
	const attempts = 12
	for attempt := range attempts {
		c := newTestClient(t)
		name := scenario(c)
		if dead := deadWithCause(t, c, name); dead != nil {
			return c, dead
		}
		t.Logf("attempt %d: tmux reported a death with no cause", attempt+1)
	}
	t.Fatalf("tmux never reported a cause of death in %d attempts", attempts)
	return nil, nil
}

// deadWithCause polls one pane for a death that carries its cause, and
// reports nil when the pane died without one.
func deadWithCause(t *testing.T, c *Client, name string) *PaneInfo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		info, err := c.PaneInfoForSession(context.Background(), name)
		if err != nil {
			t.Fatalf("PaneInfo: %v", err)
		}
		if info.Dead && (info.ExitStatus != nil || info.ExitSignal != nil) {
			return info
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}

// waitForDead returns a pane once tmux reports it dead. It says nothing about
// whether the cause of that death was published; deadWithCause covers that.
func waitForDead(t *testing.T, c *Client, name string) *PaneInfo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		info, err := c.PaneInfoForSession(context.Background(), name)
		if err != nil {
			t.Fatalf("PaneInfo: %v", err)
		}
		if info.Dead {
			return info
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane %q never became dead", name)
	return nil
}

// PaneInfo carries the whole tuple reconciliation compares. tmux 3.6a does
// not expose pane_start_time, so process-group id and start time come from
// /proc.
func TestPaneInfoCarriesTheIdentityTuple(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate(t, c, spec("ident", "/bin/sleep", "300"))
	info, err := c.PaneInfoForSession(ctx, "ident")
	if err != nil {
		t.Fatalf("PaneInfo: %v", err)
	}

	if info.SessionID == "" || !strings.HasPrefix(info.SessionID, "$") {
		t.Errorf("session id = %q, want a tmux $N id", info.SessionID)
	}
	if info.PaneID == "" || !strings.HasPrefix(info.PaneID, "%") {
		t.Errorf("pane id = %q, want a tmux %%N id", info.PaneID)
	}
	if info.PID <= 0 {
		t.Errorf("pane pid = %d", info.PID)
	}
	if info.ProcessGroupID <= 0 {
		t.Errorf("process group id = %d; without it the tuple is incomplete", info.ProcessGroupID)
	}
	if info.StartTicks <= 0 {
		t.Errorf("start ticks = %d; PIDs are reused, so start time is what makes "+
			"the tuple trustworthy", info.StartTicks)
	}
	if info.Generation != 1 {
		t.Errorf("generation = %d, want 1", info.Generation)
	}
	if info.Marker != "marker-ident" {
		t.Errorf("marker = %q, want marker-ident", info.Marker)
	}
}

// Respawn creates a new generation and new markers, so a reused tmux name
// cannot be mistaken for the previous incarnation.
func TestRespawnRecordsANewGeneration(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	before := mustCreate(t, c, spec("resp", "/bin/false"))

	next := spec("resp", "/bin/sleep", "300")
	next.Generation = 2
	next.Marker = "marker-gen2"
	after, err := c.RespawnPane(ctx, before.PaneID, next)
	if err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	if after.Generation != 2 {
		t.Errorf("generation = %d, want 2", after.Generation)
	}
	if after.Marker != "marker-gen2" {
		t.Errorf("marker = %q, want marker-gen2", after.Marker)
	}
	if after.Dead {
		t.Error("pane is still dead after respawn")
	}
	if after.PID == before.PID {
		t.Error("respawn reused the old pid; it should be a new process")
	}
}

// Input text is delivered as literal input. Nothing may expand it — not a
// shell, not tmux's key parser.
func TestSendLiteralDoesNotExpandAnything(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane := mustCreate(t, c, spec("lit", "/bin/cat"))

	payload := "plain $HOME `id` $(whoami) ; ls | wc && true"
	if err := c.SendLiteral(ctx, pane.PaneID, payload); err != nil {
		t.Fatalf("SendLiteral: %v", err)
	}
	if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	out := waitForOutput(t, c, pane.PaneID, payload)
	if !strings.Contains(out, payload) {
		t.Errorf("literal text was altered in transit.\n got: %q\nwant substring: %q", out, payload)
	}

	// Evidence of evaluation must be the EXPANSION of a construct, never a
	// substring of the payload itself — `cat` echoes the payload back, so
	// asserting on any literal fragment of it would fail for the wrong reason.
	leaks := map[string]string{
		os.Getenv("HOME"): "$HOME was expanded",
		"uid=":            "`id` was executed",
	}
	for needle, why := range leaks {
		if needle == "" {
			continue
		}
		if strings.Contains(out, needle) {
			t.Errorf("%s: text was evaluated rather than sent literally (found %q)", why, needle)
		}
	}
	// `ls | wc` would have produced a bare numeric triple on its own line.
	for _, line := range strings.Split(out, "\n") {
		if line != "" && line != payload && strings.HasPrefix(line, "   ") {
			t.Errorf("unexpected command output line %q: the pipeline ran", line)
		}
	}
}

func waitForOutput(t *testing.T, c *Client, paneID, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := c.CapturePane(context.Background(), paneID)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		last = out
		if strings.Contains(out, want) {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

func TestSendKeyRejectsKeysOutsideTheAllowlist(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("keys", "/bin/cat"))

	for _, key := range []string{"C-z", "kill-session", "", "Enter Enter"} {
		if err := c.SendKey(ctx, pane.PaneID, key); err == nil {
			t.Errorf("SendKey accepted %q, which is not in the permitted set", key)
		}
	}
}

func TestSendBracketedPasteDeliversTheBody(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("paste", "/bin/cat"))

	const body = "multi word pasted body"
	if err := c.SendBracketedPaste(ctx, pane.PaneID, body); err != nil {
		t.Fatalf("SendBracketedPaste: %v", err)
	}
	if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	out := waitForOutput(t, c, pane.PaneID, body)
	if !strings.Contains(out, body) {
		t.Errorf("pasted body missing from pane.\ngot: %q", out)
	}
}

func TestWriteObserverReportsTextAndKeyAttempts(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("observe-writes", "/bin/cat"))

	var got []WriteObservation
	c.SetWriteObserverForTest(func(observation WriteObservation) {
		got = append(got, observation)
	})
	t.Cleanup(func() { c.SetWriteObserverForTest(nil) })

	const body = "observed body"
	if err := c.SendBracketedPaste(ctx, pane.PaneID, body); err != nil {
		t.Fatalf("SendBracketedPaste: %v", err)
	}
	if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("write observations = %d, want paste start, body, paste end, and submit key", len(got))
	}
	if got[1].PaneID != pane.PaneID || string(got[1].Data) != body || got[1].Key != "" {
		t.Errorf("body observation pane/key/data length = %q/%q/%d",
			got[1].PaneID, got[1].Key, len(got[1].Data))
	}
	if got[3].PaneID != pane.PaneID || len(got[3].Data) != 0 || got[3].Key != "Enter" {
		t.Errorf("key observation pane/key/data length = %q/%q/%d",
			got[3].PaneID, got[3].Key, len(got[3].Data))
	}

	before := len(got)
	if err := c.SendKey(ctx, pane.PaneID, "not-a-key"); err == nil {
		t.Fatal("invalid key unexpectedly succeeded")
	}
	if len(got) != before {
		t.Fatalf("rejected key emitted a physical-attempt observation: %+v", got[before:])
	}

	// A valid write to a missing pane is observed even though tmux reports
	// failure: that error cannot prove the key was not delivered before the
	// acknowledgement was lost.
	if err := c.SendKey(ctx, "%999999", "Enter"); err == nil {
		t.Fatal("write to missing pane unexpectedly succeeded")
	}
	if len(got) != before+1 || got[before].PaneID != "%999999" || got[before].Key != "Enter" {
		t.Fatalf("failed physical attempt observations = %+v, want missing-pane Enter", got[before:])
	}
}

// A session name containing tmux target syntax could redirect an operation to
// a different window or pane — which for this client means killing or writing
// into the wrong one.
func TestValidateSessionNameRejectsTargetSyntax(t *testing.T) {
	bad := []string{
		"", "has:colon", "has.dot", "has$dollar", "has%percent",
		"has@at", "=exact", "has space", "has\ttab", "has\nnewline",
		strings.Repeat("x", 201),
	}
	for _, name := range bad {
		if err := ValidateSessionName(name); err == nil {
			t.Errorf("ValidateSessionName(%q) accepted a name containing target syntax", name)
		}
	}
	for _, name := range []string{"noisedeck-1", "noisedeck_2", "session-abc123", "UPPER"} {
		if err := ValidateSessionName(name); err != nil {
			t.Errorf("ValidateSessionName(%q) rejected a valid name: %v", name, err)
		}
	}
}

// tmux matches session names by prefix unless the target is '='-anchored, so
// without exact matching a lookup for "noisedeck" could operate on
// "noisedeck-extra".
func TestTargetsMatchExactlyNotByPrefix(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate(t, c, spec("noisedeck", "/bin/sleep", "300"))
	mustCreate(t, c, spec("noisedeck-extra", "/bin/sleep", "300"))

	short, err := c.PaneInfoForSession(ctx, "noisedeck")
	if err != nil {
		t.Fatalf("PaneInfoForSession noisedeck: %v", err)
	}
	if short.SessionName != "noisedeck" {
		t.Errorf("looking up %q reached session %q", "noisedeck", short.SessionName)
	}

	// Killing the short name must leave the longer one alone.
	if err := c.KillSession(ctx, "noisedeck"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	stillThere, err := c.HasSession(ctx, "noisedeck-extra")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !stillThere {
		t.Error("killing \"noisedeck\" also destroyed \"noisedeck-extra\": targets are matching by prefix")
	}
}

func TestSessionLifecycle(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// A cold socket is "we cannot look", not "there is nothing" — see
	// TestInventoryDistinguishesEmptyFromUnreachable.
	if _, err := c.HasSession(ctx, "absent"); !errors.Is(err, ErrNoServer) {
		t.Fatalf("HasSession on a cold server returned %v, want ErrNoServer", err)
	}

	for _, name := range []string{"one", "two"} {
		mustCreate(t, c, spec(name, "/bin/sleep", "300"))
	}

	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions returned %d, want 2", len(sessions))
	}
	for _, s := range sessions {
		if s.ID == "" || s.Created == 0 {
			t.Errorf("session %q missing id/created: %+v", s.Name, s)
		}
	}

	if err := c.KillSession(ctx, "one"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if exists, err := c.HasSession(ctx, "one"); err != nil || exists {
		t.Errorf("session survived kill (exists=%v err=%v)", exists, err)
	}
}

func TestOperationsOnMissingSessionReportErrNoSession(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// Start the server so failures are "no such session", not "no server".
	mustCreate(t, c, spec("anchor", "/bin/sleep", "300"))

	if _, err := c.PaneInfoForSession(ctx, "ghost"); err == nil {
		t.Error("PaneInfo succeeded for a missing session")
	}
	if err := c.KillSession(ctx, "ghost"); err == nil {
		t.Error("KillSession succeeded for a missing session")
	}
}

func TestCreateSessionValidatesItsInputs(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	cases := map[string]SessionSpec{
		"no argv":          {Name: "x", WorkDir: "/tmp", Generation: 1, Marker: "m"},
		"no workdir":       {Name: "x", Argv: []string{"/bin/true"}, Generation: 1, Marker: "m"},
		"bad name":         {Name: "a:b", WorkDir: "/tmp", Argv: []string{"/bin/true"}, Generation: 1, Marker: "m"},
		"zero generation":  {Name: "x", WorkDir: "/tmp", Argv: []string{"/bin/true"}, Marker: "m"},
		"no marker":        {Name: "x", WorkDir: "/tmp", Argv: []string{"/bin/true"}, Generation: 1},
		"missing work dir": {Name: "x", WorkDir: "/no/such/dir", Argv: []string{"/bin/true"}, Generation: 1, Marker: "m"},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.CreateSession(ctx, s); err == nil {
				t.Error("CreateSession accepted an invalid spec")
			}
		})
	}
}

func TestPaneOptionsRequireUserNamespace(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("opts", "/bin/sleep", "300"))

	// Writing a built-in option through this API would let a caller change
	// tmux behaviour such as remain-on-exit.
	if err := c.SetPaneOption(ctx, pane.PaneID, "remain-on-exit", "off"); err == nil {
		t.Error("SetPaneOption accepted a non-user option")
	}
	if _, err := c.GetPaneOption(ctx, pane.PaneID, "remain-on-exit"); err == nil {
		t.Error("GetPaneOption accepted a non-user option")
	}

	if err := c.SetPaneOption(ctx, pane.PaneID, "@custom", "value"); err != nil {
		t.Fatalf("SetPaneOption: %v", err)
	}
	got, err := c.GetPaneOption(ctx, pane.PaneID, "@custom")
	if err != nil {
		t.Fatalf("GetPaneOption: %v", err)
	}
	if got != "value" {
		t.Errorf("option = %q, want %q", got, "value")
	}
}

func TestVerifySocketRejectsLoosePermissions(t *testing.T) {
	c := newTestClient(t)

	// No socket yet: nothing to complain about.
	if err := c.VerifySocket(); err != nil {
		t.Errorf("VerifySocket before the server starts: %v", err)
	}

	mustCreate(t, c, spec("sock", "/bin/sleep", "300"))
	if err := c.VerifySocket(); err != nil {
		t.Errorf("VerifySocket on a freshly created socket: %v", err)
	}

	if err := os.Chmod(c.SocketPath(), 0o777); err != nil {
		t.Fatalf("chmod socket: %v", err)
	}
	if err := c.VerifySocket(); err == nil {
		t.Error("VerifySocket accepted a world-accessible socket")
	}
}

// tmux applies a `-f` config file ONLY when it starts a server and silently
// ignores it otherwise. A server left behind by a previous run — or started by
// anything else on this socket — would therefore have remain-on-exit off, and
// the first provider that failed to start would vanish without evidence.
func TestServerOptionsAreEnforcedOnAPreExistingServer(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// Start a server on this socket WITHOUT our config, exactly as a stale
	// process would leave one.
	raw := exec.CommandContext(ctx, "tmux", "-S", c.SocketPath(),
		"new-session", "-d", "-s", "stale", "-c", "/tmp", "--", "/bin/sleep", "300")
	if out, err := raw.CombinedOutput(); err != nil {
		t.Fatalf("seed unconfigured server: %v (%s)", err, out)
	}

	// Confirm the precondition: the option really is off.
	off := exec.CommandContext(ctx, "tmux", "-S", c.SocketPath(),
		"show-options", "-g", "-v", "remain-on-exit")
	out, err := off.Output()
	if err != nil {
		t.Fatalf("read option: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "off" {
		t.Fatalf("precondition failed: remain-on-exit is already %q", got)
	}

	// Creating through the client must fix it, and the instantly-exiting
	// session must survive.
	mustCreate(t, c, spec("victim", "/bin/false"))

	exists, err := c.HasSession(ctx, "victim")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !exists {
		t.Fatal("session vanished: the pre-existing server was never reconfigured, " +
			"so remain-on-exit was off and the exit evidence is gone")
	}
}

// A session-scoped target resolves the CURRENT window and pane, which moves.
// Pane operations must address the pane id captured at launch.
func TestPaneOperationsFollowThePaneNotTheActiveWindow(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane := mustCreate(t, c, spec("multi", "/bin/cat"))

	// Simulate the provider opening a window, which becomes current.
	raw := exec.CommandContext(ctx, "tmux", "-S", c.SocketPath(),
		"new-window", "-t", "=multi:", "--", "/bin/sleep", "300")
	if out, err := raw.CombinedOutput(); err != nil {
		t.Fatalf("open second window: %v (%s)", err, out)
	}

	// The session-scoped view now points somewhere else...
	viaSession, err := c.PaneInfoForSession(ctx, "multi")
	if err != nil {
		t.Fatalf("PaneInfoForSession: %v", err)
	}
	if viaSession.PaneID == pane.PaneID {
		t.Skip("tmux did not make the new window current; nothing to prove here")
	}

	// ...while the pane-scoped view still reaches the provider's pane.
	viaPane, err := c.PaneInfoByID(ctx, pane.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}
	if viaPane.PaneID != pane.PaneID {
		t.Errorf("pane target resolved to %q, want %q", viaPane.PaneID, pane.PaneID)
	}
	if viaPane.Marker != "marker-multi" {
		t.Errorf("marker = %q: the pane target reached the wrong pane", viaPane.Marker)
	}

	// And a write goes to the provider's pane, not the new window's.
	const probe = "written-to-the-provider-pane"
	if err := c.SendLiteral(ctx, pane.PaneID, probe); err != nil {
		t.Fatalf("SendLiteral: %v", err)
	}
	if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	if out := waitForOutput(t, c, pane.PaneID, probe); !strings.Contains(out, probe) {
		t.Errorf("text did not reach the provider pane; got %q", out)
	}
}

// A pasted body containing the bracketed-paste terminator would end paste
// mode early, turning the remainder of the message into live keystrokes.
func TestPasteBodyRejectsControlCharacters(t *testing.T) {
	bad := map[string]string{
		"paste terminator": "safe\x1b[201~rm -rf /\n",
		"bare escape":      "prefix\x1bsuffix",
		"NUL":              "a\x00b",
		"bell":             "ding\x07",
		"delete":           "oops\x7f",
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePasteBody(body); err == nil {
				t.Error("ValidatePasteBody accepted a body that can escape paste mode")
			}
		})
	}

	ok := []string{
		"plain text", "multi\nline\nbody", "tabbed\tcolumns",
		"windows\r\nline endings", "unicode ✅ é 日本語", "",
	}
	for _, body := range ok {
		if err := ValidatePasteBody(body); err != nil {
			t.Errorf("ValidatePasteBody(%q) rejected legitimate content: %v", body, err)
		}
	}
}

func TestSendBracketedPasteRefusesUnsafeBodies(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("guard", "/bin/cat"))

	if err := c.SendBracketedPaste(ctx, pane.PaneID, "before\x1b[201~after"); err == nil {
		t.Error("SendBracketedPaste sent a body containing the paste terminator")
	}
}
func TestVerifySecurityChecksDirectoryAndSocket(t *testing.T) {
	c := newTestClient(t)
	mustCreate(t, c, spec("sec", "/bin/sleep", "300"))

	if err := c.VerifySecurity(); err != nil {
		t.Fatalf("VerifySecurity on a correct setup: %v", err)
	}

	if err := os.Chmod(filepath.Dir(c.SocketPath()), 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(c.SocketPath()), 0o700) })

	if err := c.VerifySecurity(); err == nil {
		t.Error("VerifySecurity accepted a loosened socket directory")
	}
}

func TestValidatePaneIDRejectsNonPaneTargets(t *testing.T) {
	for _, bad := range []string{"", "ac", "=ac:", "%", "%a", "$0", "%1;x", "%1 "} {
		if err := ValidatePaneID(bad); err == nil {
			t.Errorf("ValidatePaneID(%q) accepted a non-pane target", bad)
		}
	}
	for _, good := range []string{"%0", "%1", "%42"} {
		if err := ValidatePaneID(good); err != nil {
			t.Errorf("ValidatePaneID(%q) rejected a valid pane id: %v", good, err)
		}
	}
}

func TestKillPaneIfIdentityRefusesBoundaryMutation(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	info := mustCreate(t, c, spec("identity-kill", "/bin/sleep", "300"))
	kept := mustCreate(t, c, spec("identity-kept", "/bin/sleep", "300"))
	identity := paneIdentity(info)
	c.testHooks = &clientTestHooks{beforeIdentityKill: func() {
		c.testHooks.beforeIdentityKill = nil
		if err := c.SetPaneOption(ctx, info.PaneID, OptMarker, "replacement-marker"); err != nil {
			t.Fatalf("replace marker at kill boundary: %v", err)
		}
	}}

	applied, err := c.KillPaneIfIdentity(ctx, identity)
	if err != nil {
		t.Fatalf("KillPaneIfIdentity mismatch: %v", err)
	}
	if applied {
		t.Fatal("identity kill applied after the pane marker changed")
	}
	current, err := c.PaneInfoByID(ctx, info.PaneID)
	if err != nil {
		t.Fatalf("mismatched pane was killed: %v", err)
	}
	applied, err = c.KillPaneIfIdentity(ctx, paneIdentity(current))
	if err != nil || !applied {
		t.Fatalf("KillPaneIfIdentity exact match: applied=%v err=%v", applied, err)
	}
	if _, err := c.PaneInfoByID(ctx, kept.PaneID); err != nil {
		t.Fatalf("sibling pane was affected: %v", err)
	}
}

func TestCreateSessionRollsBackPostSpawnFailure(t *testing.T) {
	c := newTestClient(t)
	workDir := t.TempDir()
	ready := filepath.Join(workDir, "ready")
	c.testHooks = &clientTestHooks{afterSpawn: func(kind string) error {
		if kind != "create" {
			t.Fatalf("spawn kind = %q, want create", kind)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("signal-ignoring process did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("injected post-create failure")
	}}

	info, err := c.CreateSession(context.Background(), SessionSpec{
		Name: "partial-create", WorkDir: workDir,
		Argv:       []string{"/bin/sh", "-c", `trap '' HUP TERM; : > "$1"; while :; do sleep 1; done`, "sh", ready},
		Generation: 1, Marker: "marker-partial-create",
	})
	if err == nil || info == nil {
		t.Fatalf("CreateSession = info %v err %v, want partial info and error", info, err)
	}
	assertPartialSpawnGone(t, c, info)
}

func TestRecoverCreatedPaneRollsBackIncompleteOrigin(t *testing.T) {
	for _, name := range []string{"lost-output", "incomplete-origin", "pre-setsid-origin"} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t)
			ctx := context.Background()
			s := spec(name, "/bin/sleep", "300")
			args := append(c.setOptionArgs(),
				"new-session", "-d", "-P", "-F", "#{session_id}\t#{pane_id}\t#{pane_pid}",
				"-s", s.Name, "-c", s.WorkDir, "-e", launchMarkerEnv+"="+s.Marker, "--")
			out, err := c.run(ctx, append(args, s.Argv...)...)
			if err != nil {
				t.Fatalf("create partial spawn: %v", err)
			}
			partial, err := createdPaneInfo(out, s)
			if err != nil {
				t.Fatalf("parse partial spawn: %v", err)
			}
			observed, err := c.observeSpawnedPane(ctx, partial.PaneID)
			if err != nil {
				t.Fatalf("observe partial spawn: %v", err)
			}
			partial = markSpawnOrigin(observed)
			partial.ProcessGroupID, partial.SpawnProcessGroupID = 0, 0
			switch name {
			case "lost-output":
				partial = nil
			case "incomplete-origin":
				partial.StartTicks, partial.SpawnStartTicks = 0, 0
			}

			recovered, err := c.recoverCreatedPane(ctx, s, partial)
			if err != nil || recovered == nil {
				t.Fatalf("recover partial spawn: info=%+v err=%v", recovered, err)
			}
			injected := errors.New("injected create failure")
			err = c.rollbackPartialSpawn(ctx, recovered, injected)
			if !errors.Is(err, injected) || !errors.Is(err, ErrPartialSpawnCleaned) {
				t.Fatalf("rollback = %v, want create failure and completed cleanup", err)
			}
			assertPartialSpawnGone(t, c, recovered)
		})
	}
}

func TestCreateSessionRollbackRefusesSamePaneReplacement(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const name = "partial-replacement"
	var replacement *PaneInfo
	c.testHooks = &clientTestHooks{afterMarkers: func(kind string) error {
		if kind != "create" {
			t.Fatalf("spawn kind = %q, want create", kind)
		}
		if _, err := c.run(ctx, "respawn-pane", "-k", "-t", target(name),
			"--", "/bin/sleep", "300"); err != nil {
			t.Fatalf("replace partial spawn: %v", err)
		}
		var err error
		replacement, err = c.PaneInfoForSession(ctx, name)
		if err != nil || replacement.Dead {
			t.Fatalf("observe replacement: info=%+v err=%v", replacement, err)
		}
		return errors.New("injected failure after same-pane replacement")
	}}
	t.Cleanup(func() {
		c.testHooks = nil
		_, _ = c.run(context.Background(), "kill-session", "-t", target(name))
	})

	info, err := c.CreateSession(ctx, SessionSpec{
		Name: name, WorkDir: t.TempDir(), Argv: []string{"/bin/sleep", "300"},
		Generation: 1, Marker: "marker-partial-replacement",
	})
	if err == nil || info == nil || replacement == nil {
		t.Fatalf("CreateSession = info %+v err %v replacement %+v, want refused rollback", info, err, replacement)
	}
	current, observeErr := c.PaneInfoByID(ctx, replacement.PaneID)
	if observeErr != nil || current.Dead || current.PID != replacement.PID ||
		current.StartTicks != replacement.StartTicks {
		t.Fatalf("rollback affected replacement: current=%+v replacement=%+v err=%v",
			current, replacement, observeErr)
	}
}

func TestCreateSessionRetainsPartialSpawnForDurablyFencedCleanup(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	injected := errors.New("injected final observation failure")
	c.testHooks = &clientTestHooks{afterMarkers: func(kind string) error {
		if kind != "create" {
			t.Fatalf("spawn kind = %q, want create", kind)
		}
		return injected
	}}
	info, err := c.CreateSession(ctx, SessionSpec{
		Name: "retained-partial", WorkDir: t.TempDir(),
		Argv: []string{"/bin/sleep", "300"}, Generation: 2,
		Marker: "marker-retained-partial", RetainPartialOnError: true,
	})
	if !errors.Is(err, injected) || info == nil {
		t.Fatalf("CreateSession = info %v err %v, want retained partial and injected error", info, err)
	}
	observed, err := c.PaneInfoByID(ctx, info.PaneID)
	if err != nil {
		t.Fatalf("retained candidate pane was rolled back: %v", err)
	}
	if observed.Generation != 2 || observed.Marker != "marker-retained-partial" || observed.Dead {
		t.Fatalf("retained candidate identity = %+v", observed)
	}
	alive, err := ProcAlive(observed.PID, observed.StartTicks)
	if err != nil || !alive {
		t.Fatalf("retained candidate process = alive %v err %v", alive, err)
	}
	_ = c.rollbackPartialSpawn(ctx, preserveProcessIdentity(info, observed), errors.New("test cleanup"))
	assertPartialSpawnGone(t, c, observed)
}

func TestCreateSessionRollbackKillsGroupAfterLeaderExit(t *testing.T) {
	c := newTestClient(t)
	workDir := t.TempDir()
	childFile := filepath.Join(workDir, "child")
	release := filepath.Join(workDir, "release")
	var childPID int
	var childStart int64
	c.testHooks = &clientTestHooks{afterSpawn: func(kind string) error {
		if kind != "create" {
			t.Fatalf("spawn kind = %q, want create", kind)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			raw, err := os.ReadFile(childFile)
			if err == nil {
				childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil {
					t.Fatalf("parse child pid: %v", err)
				}
				childStart = childTicks(t, childPID)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("provider child did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Fatalf("release provider leader: %v", err)
		}
		deadline = time.Now().Add(30 * time.Second)
		for {
			observed, err := c.PaneInfoForSession(context.Background(), "partial-child")
			if err != nil {
				if time.Now().After(deadline) {
					t.Fatalf("observe provider leader exit: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if observed.Dead {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("provider leader did not exit")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("injected failure after provider leader exit")
	}}

	info, err := c.CreateSession(context.Background(), SessionSpec{
		Name: "partial-child", WorkDir: workDir,
		Argv: []string{"/bin/sh", "-c", `(trap '' HUP TERM; while :; do sleep 1; done) &
echo "$!" > "$1"
while [ ! -e "$2" ]; do sleep 0.01; done
exit 0`, "sh", childFile, release},
		Generation: 1, Marker: "marker-partial-child",
	})
	if err == nil || info == nil {
		t.Fatalf("CreateSession = info %v err %v, want partial info and error", info, err)
	}
	if info.ProcessGroupID <= 1 || info.StartTicks <= 0 {
		t.Fatalf("rollback lost verified process identity: %+v", info)
	}
	assertPartialSpawnGone(t, c, info)
	alive, err := ProcAlive(childPID, childStart)
	if err != nil || alive {
		t.Fatalf("signal-resistant provider child survived: pid=%d alive=%v err=%v", childPID, alive, err)
	}
	members, err := GroupMembers(info.ProcessGroupID)
	if err != nil || len(members) != 0 {
		t.Fatalf("provider process group %d remains: members=%v err=%v",
			info.ProcessGroupID, members, err)
	}
}

func TestCreateSessionRollsBackBetweenPaneOptionWrites(t *testing.T) {
	c := newTestClient(t)
	c.testHooks = &clientTestHooks{beforeMarkerWrite: func(kind string) error {
		if kind != "create" {
			t.Fatalf("marker-write kind = %q, want create", kind)
		}
		return errors.New("injected marker-write failure")
	}}

	info, err := c.CreateSession(context.Background(), spec("partial-marker", "/bin/sleep", "300"))
	if err == nil || info == nil {
		t.Fatalf("CreateSession = info %v err %v, want partial info and error", info, err)
	}
	if info.Generation != 0 || info.Marker != "" || info.LaunchMarker == "" {
		t.Fatalf("intermediate identity = gen %d marker %q launch %q", info.Generation, info.Marker, info.LaunchMarker)
	}
	assertPartialSpawnGone(t, c, info)
}

func TestCreateSessionRollsBackInvocationErrorAfterMutation(t *testing.T) {
	c := newTestClient(t)
	c.testHooks = &clientTestHooks{afterSpawnCommand: func(kind string) error {
		if kind != "create" {
			t.Fatalf("spawn-command kind = %q, want create", kind)
		}
		return errors.New("injected create invocation error")
	}}

	info, err := c.CreateSession(context.Background(), spec("create-command-error", "/bin/sleep", "300"))
	if err == nil || info == nil {
		t.Fatalf("CreateSession = info %v err %v, want partial info and error", info, err)
	}
	assertPartialSpawnGone(t, c, info)
}

func TestRespawnPaneRollsBackPostSpawnFailure(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	dead := mustCreate(t, c, spec("partial-respawn", "/bin/sh", "-c", "exit 7"))
	deadline := time.Now().Add(30 * time.Second)
	for !dead.Dead && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		observed, err := c.PaneInfoByID(ctx, dead.PaneID)
		if err != nil {
			t.Fatalf("observe dead fixture: %v", err)
		}
		dead = observed
	}
	if !dead.Dead {
		t.Fatal("respawn fixture did not become a dead pane")
	}
	c.testHooks = &clientTestHooks{afterSpawn: func(kind string) error {
		if kind != "respawn" {
			t.Fatalf("spawn kind = %q, want respawn", kind)
		}
		return errors.New("injected post-respawn failure")
	}}

	info, err := c.RespawnPane(ctx, dead.PaneID, SessionSpec{
		WorkDir: "/tmp", Argv: []string{"/bin/sleep", "300"},
		Generation: 2, Marker: "replacement-marker",
	})
	if err == nil || info == nil {
		t.Fatalf("RespawnPane = info %v err %v, want partial info and error", info, err)
	}
	assertPartialSpawnGone(t, c, info)
}

func TestRespawnPaneRollsBackLostInvocationAcknowledgement(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	workDir := t.TempDir()
	childFile := filepath.Join(workDir, "child")
	release := filepath.Join(workDir, "release")
	var childPID int
	var childStart int64
	dead := mustCreate(t, c, spec("respawn-command-error", "/bin/sh", "-c", "exit 7"))
	deadline := time.Now().Add(30 * time.Second)
	for !dead.Dead && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		observed, err := c.PaneInfoByID(ctx, dead.PaneID)
		if err != nil {
			t.Fatalf("observe dead fixture: %v", err)
		}
		dead = observed
	}
	if !dead.Dead {
		t.Fatal("respawn fixture did not become a dead pane")
	}
	c.testHooks = &clientTestHooks{spawnRunResult: func(kind, out string, runErr error) (string, error) {
		if kind != "respawn" {
			t.Fatalf("spawn-command kind = %q, want respawn", kind)
		}
		if runErr != nil {
			t.Fatalf("physical respawn command failed before injection: %v", runErr)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			raw, err := os.ReadFile(childFile)
			if err == nil {
				childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil {
					t.Fatalf("parse respawn child pid: %v", err)
				}
				childStart = childTicks(t, childPID)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("respawn child did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
			t.Fatalf("release respawn leader: %v", err)
		}
		deadline = time.Now().Add(30 * time.Second)
		for {
			observed, err := c.PaneInfoByID(ctx, dead.PaneID)
			if err != nil {
				if time.Now().After(deadline) {
					t.Fatalf("observe rapid respawn exit: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if observed.Dead {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("rapid respawn did not exit")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return "", errors.New("injected lost respawn acknowledgement")
	}}

	info, err := c.RespawnPane(ctx, dead.PaneID, SessionSpec{
		WorkDir: workDir,
		Argv: []string{"/bin/sh", "-c", `(trap '' HUP TERM; while :; do sleep 1; done) &
echo "$!" > "$1.tmp.$$"
mv "$1.tmp.$$" "$1"
while [ ! -e "$2" ]; do sleep 0.01; done
exit 9`, "sh", childFile, release},
		Generation: 2, Marker: "replacement-marker",
	})
	if err == nil || info == nil {
		t.Fatalf("RespawnPane = info %v err %v, want partial info and error", info, err)
	}
	if info.Generation != 2 || info.Marker != "replacement-marker" {
		t.Fatalf("accepted respawn identity = gen %d marker %q", info.Generation, info.Marker)
	}
	if !info.Dead || info.PID <= 1 {
		t.Fatalf("rapid-dead respawn identity = %+v", info)
	}
	if _, err := c.PaneInfoByID(ctx, info.PaneID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("accepted rapid respawn pane remains: %v", err)
	}
	alive, err := ProcAlive(childPID, childStart)
	if err != nil || alive {
		t.Fatalf("rapid-dead respawn child survived: pid=%d alive=%v err=%v", childPID, alive, err)
	}
	members, err := GroupMembers(info.PID)
	if err != nil || len(members) != 0 {
		t.Fatalf("rapid-dead respawn group %d remains: members=%v err=%v", info.PID, members, err)
	}
}

func TestRespawnPaneQueueFailureAfterSpawnRollsBack(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	dead := mustCreate(t, c, spec("respawn-queue-failure", "/bin/sh", "-c", "exit 7"))
	deadline := time.Now().Add(30 * time.Second)
	for !dead.Dead && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		var err error
		dead, err = c.PaneInfoByID(ctx, dead.PaneID)
		if err != nil {
			t.Fatalf("observe dead fixture: %v", err)
		}
	}
	if !dead.Dead {
		t.Fatal("queue-failure fixture did not become dead")
	}
	c.testHooks = &clientTestHooks{spawnArgs: func(kind string, args []string) []string {
		if kind != "respawn" {
			t.Fatalf("spawn kind = %q, want respawn", kind)
		}
		seenRespawn := false
		for i, arg := range args {
			if arg == "respawn-pane" {
				seenRespawn = true
				continue
			}
			if seenRespawn && arg == ";" {
				injected := append([]string{}, args[:i+1]...)
				injected = append(injected,
					"set-option", "-p", "-t", "%999999", "@injected_failure", "x", ";")
				return append(injected, args[i+1:]...)
			}
		}
		t.Fatal("respawn command separator missing")
		return nil
	}}

	info, err := c.RespawnPane(ctx, dead.PaneID, SessionSpec{
		WorkDir: "/tmp", Argv: []string{"/bin/sleep", "300"},
		Generation: 2, Marker: "unpublished-marker",
	})
	if err == nil || info == nil {
		t.Fatalf("RespawnPane = info %v err %v, want changed partial and queue error", info, err)
	}
	if info.PID == dead.PID {
		t.Fatalf("queue failure did not expose physical respawn: old=%d new=%d", dead.PID, info.PID)
	}
	assertPartialSpawnGone(t, c, info)
}

func TestRespawnPaneRejectedCommandPreservesDeadPane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	dead := mustCreate(t, c, spec("respawn-rejected", "/bin/sh", "-c", "printf old-scrollback; exit 7"))
	deadline := time.Now().Add(30 * time.Second)
	for !dead.Dead && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		var err error
		dead, err = c.PaneInfoByID(ctx, dead.PaneID)
		if err != nil {
			t.Fatalf("observe dead fixture: %v", err)
		}
	}
	if !dead.Dead {
		t.Fatal("rejected-respawn fixture did not become dead")
	}
	before, err := c.CapturePaneHistory(ctx, dead.PaneID)
	if err != nil || !strings.Contains(before, "old-scrollback") {
		t.Fatalf("capture old scrollback = %q err=%v", before, err)
	}
	c.testHooks = &clientTestHooks{spawnArgs: func(kind string, args []string) []string {
		if kind != "respawn" {
			t.Fatalf("spawn kind = %q, want respawn", kind)
		}
		for i, arg := range args {
			if arg == "respawn-pane" {
				injected := append([]string{}, args[:i+1]...)
				injected = append(injected, "-Z")
				return append(injected, args[i+1:]...)
			}
		}
		t.Fatal("respawn-pane command missing from invocation")
		return nil
	}}

	info, err := c.RespawnPane(ctx, dead.PaneID, SessionSpec{
		WorkDir: "/tmp", Argv: []string{"/bin/sleep", "300"},
		Generation: 2, Marker: "rejected-marker",
	})
	if err == nil || info != nil {
		t.Fatalf("RespawnPane = info %v err %v, want rejected command", info, err)
	}
	after, observeErr := c.PaneInfoByID(ctx, dead.PaneID)
	if observeErr != nil {
		t.Fatalf("rejected respawn removed old pane: %v", observeErr)
	}
	if after.Generation != dead.Generation || after.Marker != dead.Marker || !after.Dead {
		t.Fatalf("rejected respawn changed old identity: before=%+v after=%+v", dead, after)
	}
	afterScrollback, err := c.CapturePaneHistory(ctx, dead.PaneID)
	if err != nil || !strings.Contains(afterScrollback, "old-scrollback") {
		t.Fatalf("old scrollback after rejection = %q err=%v", afterScrollback, err)
	}
}

func assertPartialSpawnGone(t *testing.T, c *Client, info *PaneInfo) {
	t.Helper()
	if _, err := c.PaneInfoByID(context.Background(), info.PaneID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("partial pane %s remains: %v", info.PaneID, err)
	}
	if info.PID <= 0 || info.StartTicks <= 0 {
		t.Fatalf("partial spawn lacks process identity: %+v", info)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		alive, err := ProcAlive(info.PID, info.StartTicks)
		if err != nil {
			t.Fatalf("partial process liveness: %v", err)
		}
		if !alive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("partial process %d remains alive", info.PID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestCreateSessionReturnsItsOwnPaneID(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane := mustCreate(t, c, spec("owned", "/bin/sleep", "300"))
	if err := ValidatePaneID(pane.PaneID); err != nil {
		t.Fatalf("CreateSession returned %q: %v", pane.PaneID, err)
	}

	byID, err := c.PaneInfoByID(ctx, pane.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}
	if byID.Marker != "marker-owned" {
		t.Errorf("marker = %q: the returned pane id is not the created pane", byID.Marker)
	}
	if byID.PID != pane.PID {
		t.Errorf("pid mismatch: created %d, read back %d", pane.PID, byID.PID)
	}
}
func TestKillSessionDestroysExitEvidence(t *testing.T) {
	ctx := context.Background()

	// The point of this test is what kill-session does to evidence that
	// EXISTS, so the death is repeated until tmux actually reports one.
	c, dead := untilCauseReported(t, func(c *Client) string {
		pane := mustCreate(t, c, spec("evidence", "/bin/sleep", "300"))
		info, err := c.PaneInfoByID(ctx, pane.PaneID)
		if err != nil {
			t.Fatalf("PaneInfoByID: %v", err)
		}
		if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil {
			t.Fatalf("signal: %v", err)
		}
		return "evidence"
	})
	if dead.ExitSignal == nil {
		t.Fatal("a reported cause carried no signal for a signalled death")
	}
	recorded := *dead.ExitSignal

	if err := c.KillSession(ctx, "evidence"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	if _, err := c.PaneInfoByID(ctx, dead.PaneID); err == nil {
		t.Error("the dead pane survived kill-session; if tmux changes this, the " +
			"requirement to persist exit evidence before removing the pane can be relaxed")
	}
	t.Logf("exit signal %d was readable before kill-session and unreadable after: "+
		"stopping must persist exit evidence before removing the pane", recorded)
}

// kill-session does not stop the process, so it is not a stop mechanism.
func TestKillSessionLeavesASignalIgnoringProcessAlive(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane := mustCreate(t, c, spec("clings",
		"/bin/sh", "-c", `trap "" HUP TERM; sleep 300`))
	info, err := c.PaneInfoByID(ctx, pane.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(info.PID, syscall.SIGKILL) })

	if err := c.KillSession(ctx, "clings"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := syscall.Kill(info.PID, 0); err != nil {
		t.Fatalf("PREMISE CHANGED: the process did NOT survive kill-session (%v). "+
			"Session stopping is designed around kill-session being "+
			"insufficient to stop a provider. If tmux now terminates the pane "+
			"process group, revisit that design rather than deleting this test.", err)
	}
	t.Logf("pid %d survived kill-session: terminating a provider requires "+
		"signalling it, which needs recorded identity and force authorization", info.PID)
}

// A pane can vanish from a REACHABLE server while its process keeps running
// with an unchanged start time. So "tmux no longer tracks it" never implies
// "the process exited"; only /proc can answer that.
func TestPaneCanVanishWhileItsProcessSurvives(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate(t, c, spec("anchor", "/bin/sleep", "300")) // keeps the server up
	pane := mustCreate(t, c, spec("vanisher",
		"/bin/sh", "-c", `trap "" HUP TERM; sleep 300`))
	info, err := c.PaneInfoByID(ctx, pane.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(info.PID, syscall.SIGKILL) })

	if err := c.KillSession(ctx, "vanisher"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if err := syscall.Kill(info.PID, 0); err != nil {
		t.Fatalf("PREMISE CHANGED: the process did not survive kill-session (%v); "+
			"the claim that a missing pane is not evidence of exit rests on it", err)
	}

	// The server is reachable — the anchor proves it — yet the pane is absent.
	if _, err := c.ListSessions(ctx); err != nil {
		t.Fatalf("server should still be reachable: %v", err)
	}
	if _, err := c.PaneInfoByID(ctx, pane.PaneID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrNoSession on a live server, got %v", err)
	}

	_, ticks, _, err := processIdentity(info.PID)
	if err != nil {
		t.Fatalf("processIdentity: %v", err)
	}
	if ticks != info.StartTicks {
		t.Errorf("start ticks changed (%d -> %d); the pid was reused", info.StartTicks, ticks)
	}
	t.Log("pane absent from a reachable server while the same process runs on: " +
		"ErrNoSession is not evidence of exit")
}

// tmux ids are unique only within one server. A restarted server
// reallocates $0 and %0, which is why identity is the whole tuple and not a
// captured pane id alone.
func TestServerRestartReusesSessionAndPaneIDs(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	first := mustCreate(t, c, spec("gen1", "/bin/sleep", "300"))
	if err := c.KillServer(ctx); err != nil {
		t.Fatalf("KillServer: %v", err)
	}
	second := mustCreate(t, c, spec("gen2", "/bin/sleep", "300"))

	if first.PaneID != second.PaneID || first.SessionID != second.SessionID {
		t.Fatalf("PREMISE CHANGED: a restarted server did not reuse ids "+
			"(%s/%s then %s/%s). Process identity still requires the full tuple, but the "+
			"specific hazard this test records no longer reproduces — confirm "+
			"before relying on id stability anywhere.",
			first.SessionID, first.PaneID, second.SessionID, second.PaneID)
	}
	if first.PID == second.PID {
		t.Fatal("both servers reported the same pid; test setup is wrong")
	}
	t.Logf("both servers allocated %s/%s for different pids (%d, %d): a captured "+
		"pane id is not identity on its own",
		second.SessionID, second.PaneID, first.PID, second.PID)
}

// An unreachable server must not look like an empty inventory. Reconciliation
// has to tell "every session is gone" from "we could not look".
func TestInventoryDistinguishesEmptyFromUnreachable(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// Cold socket: no server at all.
	if _, err := c.ListSessions(ctx); !errors.Is(err, ErrNoServer) {
		t.Errorf("ListSessions on a cold socket returned %v, want ErrNoServer", err)
	}
	if _, err := c.ListPanes(ctx); !errors.Is(err, ErrNoServer) {
		t.Errorf("ListPanes on a cold socket returned %v, want ErrNoServer", err)
	}
	if _, err := c.HasSession(ctx, "anything"); !errors.Is(err, ErrNoServer) {
		t.Errorf("HasSession on a cold socket returned %v, want ErrNoServer", err)
	}

	// A live server with a session: an authoritative, non-empty inventory.
	mustCreate(t, c, spec("present", "/bin/sleep", "300"))
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("got %d sessions, want 1", len(sessions))
	}
	exists, err := c.HasSession(ctx, "absent-but-server-is-up")
	if err != nil {
		t.Errorf("HasSession for a missing session errored: %v", err)
	}
	if exists {
		t.Error("HasSession reported a session that does not exist")
	}
}

// Reconciliation must spot panes in the namespace that no recorded session
// claims, which per-session lookups can never do.
func TestListPanesEnumeratesEveryPaneIncludingExtraWindows(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	a := mustCreate(t, c, spec("one", "/bin/sleep", "300"))
	b := mustCreate(t, c, spec("two", "/bin/sleep", "300"))

	// A pane Deck did not create, in a session it did.
	raw := exec.CommandContext(ctx, "tmux", "-S", c.SocketPath(),
		"new-window", "-t", "=one:", "--", "/bin/sleep", "300")
	if out, err := raw.CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v (%s)", err, out)
	}

	panes, err := c.ListPanes(ctx)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 3 {
		t.Fatalf("got %d panes, want 3 (two created plus one unmanaged)", len(panes))
	}

	byID := map[string]PaneRecord{}
	for _, p := range panes {
		byID[p.PaneID] = p
	}
	for _, want := range []*PaneInfo{a, b} {
		got, ok := byID[want.PaneID]
		if !ok {
			t.Errorf("pane %s missing from the inventory", want.PaneID)
			continue
		}
		if got.Marker != want.Marker || got.Generation != want.Generation {
			t.Errorf("pane %s: marker/generation = %q/%d, want %q/%d",
				got.PaneID, got.Marker, got.Generation, want.Marker, want.Generation)
		}
	}

	// The unmanaged pane is visible and carries no marker — which is exactly
	// what lets reconciliation flag it.
	var unmanaged int
	for _, p := range panes {
		if p.Marker == "" {
			unmanaged++
		}
	}
	if unmanaged != 1 {
		t.Errorf("found %d unmarked panes, want 1", unmanaged)
	}
}

func TestGroupMembersFindsTheLeaderAndRejectsUnusableGroups(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane := mustCreate(t, c, spec("grp", "/bin/sleep", "300"))
	info, err := c.PaneInfoByID(ctx, pane.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}

	members, err := GroupMembers(info.ProcessGroupID)
	if err != nil {
		t.Fatalf("GroupMembers: %v", err)
	}
	var found bool
	for _, pid := range members {
		if pid == info.PID {
			found = true
		}
	}
	if !found {
		t.Errorf("group %d does not contain its leader %d (members=%v)",
			info.ProcessGroupID, info.PID, members)
	}

	for _, bad := range []int{0, 1, -5} {
		if _, err := GroupMembers(bad); err == nil {
			t.Errorf("GroupMembers(%d) accepted an unusable group id", bad)
		}
	}
}

// tmux treats a standalone ";" argument as a command-list separator even after
// "--", so `send-keys -l -- ";"` reports success and delivers nothing. A
// pasted message of exactly ";" would be silently lost. SendLiteral
// hex-encodes to avoid the parser entirely; these bodies would all fail under
// the string form.
func TestSendLiteralDeliversTmuxMetacharacters(t *testing.T) {
	bodies := map[string]string{
		"bare semicolon":     ";",
		"double semicolon":   ";;",
		"escaped semicolon":  `\;`,
		"leading semicolon":  "; echo hi",
		"semicolon in token": "x;y",
		"braces":             "{ }",
		"dollar and hash":    "$HOME #comment",
		"quotes":             `"double" 'single'`,
		"backslashes":        `a\b\\c`,
		"unicode":            "héllo ✅ 日本語",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t)
			ctx := context.Background()
			pane := mustCreate(t, c, spec("meta", "/bin/cat"))

			if err := c.SendLiteral(ctx, pane.PaneID, body); err != nil {
				t.Fatalf("SendLiteral(%q): %v", body, err)
			}
			if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
				t.Fatalf("SendKey: %v", err)
			}
			if out := waitForOutput(t, c, pane.PaneID, body); !strings.Contains(out, body) {
				t.Errorf("body %q did not reach the pane; captured %q", body, out)
			}
		})
	}
}

// Text longer than one send-keys invocation must arrive intact.
func TestSendLiteralDeliversTextLongerThanOneChunk(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	pane := mustCreate(t, c, spec("long", "/bin/cat"))

	// Must exceed maxBytesPerSend (512) so it spans chunks, but stay small
	// enough that cat's echo does not scroll off an 80x24 pane.
	body := strings.Repeat("abcdefghij", 60) // 600 bytes
	if err := c.SendLiteral(ctx, pane.PaneID, body); err != nil {
		t.Fatalf("SendLiteral: %v", err)
	}
	if err := c.SendKey(ctx, pane.PaneID, "Enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	// cat wraps output across pane lines, so compare with wrapping removed.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.CapturePane(ctx, pane.PaneID)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if strings.Contains(strings.ReplaceAll(out, "\n", ""), body) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("%d-byte body did not arrive intact across chunk boundaries (maxBytesPerSend=%d)", len(body), maxBytesPerSend)
}

// A reachable server with zero sessions is a normal steady state here, because
// exit-empty is off. It must read as an empty inventory, not as an error.
func TestReachableServerWithZeroSessionsReadsAsEmpty(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	mustCreate(t, c, spec("only", "/bin/sleep", "300"))
	if err := c.KillSession(ctx, "only"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Errorf("ListSessions on a reachable empty server: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}

	panes, err := c.ListPanes(ctx)
	if err != nil {
		t.Errorf("ListPanes on a reachable empty server: %v", err)
	}
	if len(panes) != 0 {
		t.Errorf("got %d panes, want 0", len(panes))
	}

	exists, err := c.HasSession(ctx, "anything")
	if err != nil {
		t.Errorf("HasSession on a reachable empty server: %v", err)
	}
	if exists {
		t.Error("HasSession reported a session on an empty server")
	}
}

// paneInMode reads tmux's own pane_in_mode flag for a pane — tmux-owned
// state, which is the only kind of pane state this product may consult.
func paneInMode(t *testing.T, c *Client, paneID string) string {
	t.Helper()
	out, err := c.run(context.Background(), "display-message", "-p",
		"-t", paneTarget(paneID), "#{pane_in_mode}")
	if err != nil {
		t.Fatalf("read pane_in_mode: %v", err)
	}
	return out
}

func TestCopyModeScrollEntersAndExitsTheTmuxOwnedView(t *testing.T) {
	c := newTestClient(t)
	info := mustCreate(t, c, spec("scrolls", "/bin/sleep", "300"))
	ctx := context.Background()

	// Scroll-down outside any mode is a guarded no-op: nothing to scroll
	// down through, and no error to kill an attachment over.
	active, err := c.CopyModeScrollDownState(ctx, info.PaneID, 3)
	if err != nil {
		t.Fatalf("CopyModeScrollDown outside a mode: %v", err)
	}
	if active {
		t.Fatal("CopyModeScrollDownState reported copy-mode after the guarded no-op")
	}
	if got := paneInMode(t, c, info.PaneID); got != "0" {
		t.Fatalf("pane_in_mode after no-op scroll-down = %q, want 0", got)
	}

	// Scroll-up enters copy-mode (even with an empty history the mode is
	// entered; how far the view can move is tmux's business).
	active, err = c.CopyModeScrollUpState(ctx, info.PaneID, 3)
	if err != nil {
		t.Fatalf("CopyModeScrollUp: %v", err)
	}
	if !active {
		t.Fatal("CopyModeScrollUpState did not report the entered copy-mode")
	}
	if got := paneInMode(t, c, info.PaneID); got != "1" {
		t.Fatalf("pane_in_mode after scroll-up = %q, want 1", got)
	}

	// A second scroll-up on a pane already in copy-mode must not error:
	// copy-mode entry is idempotent.
	if err := c.CopyModeScrollUp(ctx, info.PaneID, 2); err != nil {
		t.Fatalf("CopyModeScrollUp while in copy-mode: %v", err)
	}

	// Scrolling back to the bottom exits copy-mode (-e entry), returning
	// the pane to the live TUI.
	active, err = c.CopyModeScrollDownState(ctx, info.PaneID, MaxCopyModeScrollLines)
	if err != nil {
		t.Fatalf("CopyModeScrollDown: %v", err)
	}
	if active {
		t.Fatal("CopyModeScrollDownState reported copy-mode after reaching live")
	}
	if got := paneInMode(t, c, info.PaneID); got != "0" {
		t.Fatalf("pane_in_mode after scrolling to bottom = %q, want 0", got)
	}

	if err := c.CopyModeScrollUp(ctx, "not-a-pane", 1); err == nil {
		t.Error("CopyModeScrollUp accepted an invalid pane id")
	}
	if err := c.CopyModeScrollDown(ctx, "not-a-pane", 1); err == nil {
		t.Error("CopyModeScrollDown accepted an invalid pane id")
	}
	if _, err := c.ExitCopyModeState(ctx, "not-a-pane"); err == nil {
		t.Error("ExitCopyModeState accepted an invalid pane id")
	}

	if err := c.CopyModeScrollUp(ctx, info.PaneID, 1); err != nil {
		t.Fatalf("CopyModeScrollUp before ExitCopyModeState: %v", err)
	}
	active, err = c.ExitCopyModeState(ctx, info.PaneID)
	if err != nil {
		t.Fatalf("ExitCopyModeState: %v", err)
	}
	if active {
		t.Fatal("ExitCopyModeState reported copy-mode after cancel")
	}
}

func TestServerConfigAppliesTheConfiguredScrollbackDepth(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	c, err := NewClient(Config{
		SocketPath:      filepath.Join(dir, "ac.sock"),
		Timeout:         10 * time.Second,
		ScrollbackLines: 4321,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.KillServer(context.Background()) })

	info := mustCreate(t, c, spec("depth", "/bin/sleep", "300"))
	ctx := context.Background()

	got, err := c.run(ctx, "show-options", "-g", "-v", "history-limit")
	if err != nil {
		t.Fatalf("read history-limit: %v", err)
	}
	if got != "4321" {
		t.Errorf("global history-limit = %q, want 4321", got)
	}
	// The limit must have been in force BEFORE the pane was created —
	// tmux stamps history-limit per pane at creation.
	limit, err := c.run(ctx, "display-message", "-p", "-t",
		paneTarget(info.PaneID), "#{history_limit}")
	if err != nil {
		t.Fatalf("read pane history_limit: %v", err)
	}
	if limit != "4321" {
		t.Errorf("pane history_limit = %q, want 4321", limit)
	}
}

// The clamp is the availability control between a browser frame
// and tmux's -N repeat count — an unclamped count in the hundreds of
// millions wedges the whole tmux server. It must hold
// at every boundary.
func TestClampScrollLinesBoundsHostileCounts(t *testing.T) {
	cases := []struct{ in, want int }{
		{-2_000_000_000, 1},
		{-1, 1},
		{0, 1},
		{1, 1},
		{3, 3},
		{MaxCopyModeScrollLines, MaxCopyModeScrollLines},
		{MaxCopyModeScrollLines + 1, MaxCopyModeScrollLines},
		{100_000_000, MaxCopyModeScrollLines},
		{int(^uint(0) >> 1), MaxCopyModeScrollLines}, // MaxInt
	}
	for _, c := range cases {
		if got := clampScrollLines(c.in); got != c.want {
			t.Errorf("clampScrollLines(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ExitCopyMode returns a pane showing a tmux view to the live
// application, and is a guarded no-op otherwise.
func TestExitCopyModeLeavesTheViewAndToleratesItsAbsence(t *testing.T) {
	c := newTestClient(t)
	info := mustCreate(t, c, spec("exits", "/bin/sleep", "300"))
	ctx := context.Background()

	// Not in a mode: a guarded no-op, never a "not in a mode" error.
	if err := c.ExitCopyMode(ctx, info.PaneID); err != nil {
		t.Fatalf("ExitCopyMode outside a mode: %v", err)
	}

	if err := c.CopyModeScrollUp(ctx, info.PaneID, 3); err != nil {
		t.Fatalf("CopyModeScrollUp: %v", err)
	}
	if got := paneInMode(t, c, info.PaneID); got != "1" {
		t.Fatalf("pane_in_mode after scroll-up = %q, want 1", got)
	}
	if err := c.ExitCopyMode(ctx, info.PaneID); err != nil {
		t.Fatalf("ExitCopyMode: %v", err)
	}
	if got := paneInMode(t, c, info.PaneID); got != "0" {
		t.Fatalf("pane_in_mode after ExitCopyMode = %q, want 0", got)
	}

	// PaneInfo carries tmux's pane_in_mode flag for the write preflight.
	before, err := c.PaneInfoByID(ctx, info.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID: %v", err)
	}
	if before.InMode {
		t.Error("PaneInfo.InMode = true for a pane not in a mode")
	}
	if err := c.CopyModeScrollUp(ctx, info.PaneID, 1); err != nil {
		t.Fatalf("CopyModeScrollUp: %v", err)
	}
	during, err := c.PaneInfoByID(ctx, info.PaneID)
	if err != nil {
		t.Fatalf("PaneInfoByID in mode: %v", err)
	}
	if !during.InMode {
		t.Error("PaneInfo.InMode = false for a pane in copy-mode")
	}

	if err := c.ExitCopyMode(ctx, "not-a-pane"); err == nil {
		t.Error("ExitCopyMode accepted an invalid pane id")
	}
}

func historyLimit(t *testing.T, c *Client, paneID string) int {
	t.Helper()
	got, err := c.run(context.Background(), "display-message", "-p",
		"-t", paneTarget(paneID), "#{history_limit}")
	if err != nil {
		t.Fatalf("read pane history_limit: %v", err)
	}
	n, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("pane history_limit %q: %v", got, err)
	}
	return n
}

func historySize(t *testing.T, c *Client, paneID string) int {
	t.Helper()
	got, err := c.run(context.Background(), "display-message", "-p",
		"-t", paneTarget(paneID), "#{history_size}")
	if err != nil {
		t.Fatalf("read pane history_size: %v", err)
	}
	n, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("pane history_size %q: %v", got, err)
	}
	return n
}

func seqSpec(name string, count int) SessionSpec {
	return SessionSpec{
		Name:       name,
		WorkDir:    "/tmp",
		Argv:       []string{"/bin/sh", "-c", fmt.Sprintf("seq 1 %d; sleep 300", count)},
		Generation: 1,
		Marker:     "marker-" + name,
	}
}

func captureNumberedLines(t *testing.T, c *Client, paneID string, count int) (min, max, total int) {
	t.Helper()
	final := strconv.Itoa(count)
	deadline := time.Now().Add(10 * time.Second)
	var history string
	for time.Now().Before(deadline) {
		h, err := c.CapturePaneHistory(context.Background(), paneID)
		if err != nil {
			t.Fatalf("CapturePaneHistory: %v", err)
		}
		history = h
		if strings.Contains(h, final) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(history, final) {
		t.Fatalf("line %q never rendered; last capture:\n%s", final, history)
	}
	nums := make([]int, 0, 256)
	for _, line := range strings.Split(history, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		t.Fatalf("no numbered lines captured from pane %s:\n%s", paneID, history)
	}
	min, max = nums[0], nums[0]
	for _, n := range nums[1:] {
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	return min, max, len(nums)
}

func TestDefaultScrollbackStampsNewPanesAtTwoThousandLines(t *testing.T) {
	c := newTestClient(t)
	const produced = 3000
	info := mustCreate(t, c, seqSpec("limit-stream", produced))
	if got := historyLimit(t, c, info.PaneID); got != 2000 {
		t.Fatalf("pane history_limit = %d, want 2000", got)
	}

	min, max, total := captureNumberedLines(t, c, info.PaneID, produced)
	if got := historySize(t, c, info.PaneID); got > 2000 {
		t.Errorf("pane history_size = %d, want <= 2000", got)
	}
	if max != produced {
		t.Errorf("newest retained line = %d, want %d (every produced line should be captured)", max, produced)
	}
	// The visible screen is additional to history-limit, so allow its rows.
	if min < 900 || min > 1100 {
		t.Errorf("oldest retained line = %d, want within [900,1100] under a 2,000-line limit", min)
	}
	if total > 2050 {
		t.Errorf("retained line count = %d, want <= ~2,050 under a 2,000-line limit", total)
	}
}

func TestRolloutLeavesExistingPanesAtTheirBirthLimit(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	socket := filepath.Join(dir, "ac.sock")
	ctx := context.Background()

	legacy, err := NewClient(Config{
		SocketPath:      socket,
		Timeout:         10 * time.Second,
		ScrollbackLines: 10000,
	})
	if err != nil {
		t.Fatalf("legacy NewClient: %v", err)
	}
	t.Cleanup(func() { _ = legacy.KillServer(ctx) })

	const produced = 3000
	preExisting := mustCreate(t, legacy, seqSpec("rollout-legacy", produced))
	if got := historyLimit(t, legacy, preExisting.PaneID); got != 10000 {
		t.Fatalf("legacy pane history_limit = %d, want 10000", got)
	}
	minBefore, maxBefore, totalBefore := captureNumberedLines(t, legacy, preExisting.PaneID, produced)
	if minBefore != 1 {
		t.Fatalf("legacy oldest retained line = %d, want 1 under a 10,000-line limit", minBefore)
	}
	if maxBefore != produced || totalBefore < produced {
		t.Fatalf("legacy retained max=%d total=%d, want all %d produced lines", maxBefore, totalBefore, produced)
	}

	current, err := NewClient(Config{
		SocketPath:      socket,
		Timeout:         10 * time.Second,
		ScrollbackLines: 2000,
	})
	if err != nil {
		t.Fatalf("current NewClient: %v", err)
	}
	fresh := mustCreate(t, current, spec("rollout-fresh", "/bin/sleep", "300"))
	if got := historyLimit(t, current, fresh.PaneID); got != 2000 {
		t.Errorf("freshly created pane history_limit = %d, want 2000", got)
	}

	info, err := legacy.PaneInfoByID(ctx, preExisting.PaneID)
	if err != nil {
		t.Fatalf("legacy pane no longer readable after rollout: %v", err)
	}
	if info.Dead {
		t.Fatal("the legacy pane is dead after the rollout — existing panes must be left alive")
	}
	if got := historyLimit(t, legacy, preExisting.PaneID); got != 10000 {
		t.Errorf("legacy pane history_limit = %d after rollout, want 10000 (birth limit is immutable)", got)
	}
	minAfter, _, totalAfter := captureNumberedLines(t, legacy, preExisting.PaneID, produced)
	if minAfter != 1 || totalAfter < produced {
		t.Errorf("legacy history was truncated by the rollout: min=%d total=%d, want line 1 still retained and all %d lines present",
			minAfter, totalAfter, produced)
	}
}

// A pane child can lose the race with its own exec, leaving creation without
// a process-table identity. The first complete observation of the same pid
// must anchor the spawn origin instead of reading as a pane replacement.
func TestPreserveSpawnOriginObservationAnchorsIncompleteOrigin(t *testing.T) {
	prior := &PaneInfo{PaneID: "%1", PID: 4242, SpawnPID: 4242}
	observed := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4242,
		StartTicks: 99, Executable: "bash"}
	merged := preserveSpawnOriginObservation(prior, observed)
	if merged.SpawnProcessGroupID != 4242 || merged.SpawnStartTicks != 99 {
		t.Fatalf("origin not anchored: spawn pgid=%d ticks=%d",
			merged.SpawnProcessGroupID, merged.SpawnStartTicks)
	}
	if !merged.MatchesSpawnOrigin() {
		t.Fatal("repaired origin still mismatches its spawn origin")
	}
}

// Once the origin carries a complete tuple, later observations must compare
// against it, so drift still reads as a replacement.
func TestPreserveSpawnOriginObservationKeepsAnchoredOrigin(t *testing.T) {
	prior := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4242, StartTicks: 99,
		SpawnPID: 4242, SpawnProcessGroupID: 4242, SpawnStartTicks: 99}
	observed := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4242, StartTicks: 100}
	merged := preserveSpawnOriginObservation(prior, observed)
	if merged.SpawnStartTicks != 99 {
		t.Fatalf("anchored origin was overwritten: spawn ticks=%d", merged.SpawnStartTicks)
	}
	if merged.MatchesSpawnOrigin() {
		t.Fatal("start-time drift must still read as a replacement")
	}
}

// The child can also lose the race with its own setsid: creation reads the
// process table while the leader still sits in the tmux server's group. The
// same process settling as its own group leader must anchor the origin.
func TestPreserveSpawnOriginObservationSettlesPreSetsidGroup(t *testing.T) {
	prior := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4240, StartTicks: 99,
		SpawnPID: 4242, SpawnProcessGroupID: 4240, SpawnStartTicks: 99}
	observed := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4242, StartTicks: 99}
	merged := preserveSpawnOriginObservation(prior, observed)
	if merged.SpawnProcessGroupID != 4242 {
		t.Fatalf("settled group not anchored: spawn pgid=%d", merged.SpawnProcessGroupID)
	}
	if !merged.MatchesSpawnOrigin() {
		t.Fatal("settled leader still mismatches its spawn origin")
	}
}

// A settled group anchors only the SAME process: a recycled pid carrying
// different start ticks is a replacement, never an anchor.
func TestPreserveSpawnOriginObservationSettledGroupNeedsSameTicks(t *testing.T) {
	prior := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4240, StartTicks: 99,
		SpawnPID: 4242, SpawnProcessGroupID: 4240, SpawnStartTicks: 99}
	observed := &PaneInfo{PaneID: "%1", PID: 4242, ProcessGroupID: 4242, StartTicks: 100}
	merged := preserveSpawnOriginObservation(prior, observed)
	if merged.SpawnProcessGroupID != 4240 || merged.SpawnStartTicks != 99 {
		t.Fatalf("recycled pid must not anchor: %+v", merged)
	}
	if merged.MatchesSpawnOrigin() {
		t.Fatal("start-time drift must still read as a replacement")
	}
}

// An observation of a different pid is a replacement signal, never an anchor.
func TestPreserveSpawnOriginObservationIgnoresDifferentPID(t *testing.T) {
	prior := &PaneInfo{PaneID: "%1", PID: 4242, SpawnPID: 4242}
	observed := &PaneInfo{PaneID: "%1", PID: 9999, ProcessGroupID: 9999, StartTicks: 5}
	merged := preserveSpawnOriginObservation(prior, observed)
	if merged.SpawnPID != 4242 || merged.SpawnProcessGroupID != 0 {
		t.Fatalf("different-pid observation must not anchor: %+v", merged)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	experimentapi "github.com/noisefloor-oss/deck/internal/experiment"
)

func TestValidateControlURLAllowsOnlyLoopbackHTTPOrigin(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1:8837", "http://example.test:8837", "http://127.0.0.1:8837/path",
		"http://user@127.0.0.1:8837", "http://127.0.0.1:8837?x=1",
	} {
		if _, err := validateControlURL(raw); err == nil {
			t.Errorf("validateControlURL(%q) succeeded", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8837", "http://[::1]:8837/"} {
		if _, err := validateControlURL(raw); err != nil {
			t.Errorf("validateControlURL(%q): %v", raw, err)
		}
	}
}

func TestHarnessListReturnsStructuredExperimentResponse(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/experimental/harnesses" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get(experimentapi.CapabilityHeader); got != "test-capability" {
			t.Fatalf("capability header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unconfined":true,"profiles":[]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := runHarnesses([]string{"list", "--server", server.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != `{"unconfined":true,"profiles":[]}` {
		t.Fatalf("stdout = %s", got)
	}
}

func TestHarnessShowAcceptsIDBeforeFlags(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/experimental/harnesses/claude" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"unconfined":true,"evidence":{"provider":"claude"}}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := runHarnesses([]string{"show", "claude", "--server", server.URL, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
}

func TestAgentHandoffSucceedsOnAcceptanceThenCompletion(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	shim, _ := installLogstreamDouble(t)
	server := newHandoffStubServer(t)
	defer server.Close()

	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
		time.Sleep(100 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.completed", "handoff-fresh", "applied")
	}()

	args := []string{"--server", server.URL, "--project-id", "p1", "--provider", "claude",
		"--model", "fable", "--role", "reviewer", "--task", "295",
		"--correlation-id", "operator-route", "--timeout", "5s", "--completion-timeout", "10s"}
	var stdout, stderr bytes.Buffer
	if code := runAgentHandoff(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}
	for _, want := range []string{
		`"accepted": true`, `"completed": true`, `"status": "applied"`,
		`"type": "handoff.completed"`, `"correlation_id": "handoff-fresh"`, `"id": "s1"`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not contain %s: %s", want, stdout.String())
		}
	}
}

func TestAgentHandoffCompletionFailedStatusExitsNonzero(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	shim, _ := installLogstreamDouble(t)
	server := newHandoffStubServer(t)
	defer server.Close()

	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
		time.Sleep(100 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.completed", "handoff-fresh", "failed")
	}()

	args := []string{"--server", server.URL, "--project-id", "p1", "--provider", "claude",
		"--model", "fable", "--role", "reviewer", "--task", "295",
		"--correlation-id", "operator-route", "--timeout", "5s", "--completion-timeout", "10s"}
	var stdout, stderr bytes.Buffer
	if code := runAgentHandoff(args, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}
	for _, want := range []string{
		`"accepted": true`, `"completed": true`, `"status": "failed"`, "declared the task failed",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not contain %s: %s", want, stdout.String())
		}
	}
}

func TestCompletionTimeoutLeavesVisibleSessionAndFails(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	shim, _ := installLogstreamDouble(t)
	server := newHandoffStubServer(t)
	defer server.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
	}()

	args := []string{"--server", server.URL, "--project-id", "p1", "--provider", "claude",
		"--model", "fable", "--role", "reviewer", "--task", "295",
		"--correlation-id", "operator-route", "--timeout", "5s", "--completion-timeout", "400ms"}
	var stdout, stderr bytes.Buffer
	if code := runAgentHandoff(args, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}
	for _, want := range []string{
		`"accepted": true`, `"completed": false`, `"id": "s1"`,
		"delegated session remains visible but did not declare completion before timeout",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not contain %s: %s", want, stdout.String())
		}
	}
}

func TestCompletionRejectsStaleRouteCorrelationAndOrder(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "test-capability")
	shim, _ := installLogstreamDouble(t)
	server := newHandoffStubServer(t)
	defer server.Close()

	// A completion carrying the caller's requested correlation can never
	// satisfy the fresh server-generated route.
	if _, err := doubleAppend(shim, doubleStream, "handoff.completed", "operator-route", "applied"); err != nil {
		t.Fatal(err)
	}
	// A completion on the fresh correlation that predates its acceptance is
	// stale by append order.
	if _, err := doubleAppend(shim, doubleStream, "handoff.completed", "handoff-fresh", "applied"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
		time.Sleep(100 * time.Millisecond)
		// Fresh correlation after acceptance, but a route from another project.
		_, _ = doubleAppend(shim, "project/other", "handoff.completed", "handoff-fresh", "applied")
	}()

	args := []string{"--server", server.URL, "--project-id", "p1", "--provider", "claude",
		"--model", "fable", "--role", "reviewer", "--task", "295",
		"--correlation-id", "operator-route", "--timeout", "5s", "--completion-timeout", "800ms"}
	var stdout, stderr bytes.Buffer
	if code := runAgentHandoff(args, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"completed": false`) ||
		!strings.Contains(stdout.String(), "did not declare completion before timeout") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestCompletionRejectsNonClosedStatus(t *testing.T) {
	shim, _ := installLogstreamDouble(t)
	acceptanceID, err := doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doubleAppend(shim, doubleStream, "handoff.completed", "handoff-fresh", "claimed"); err != nil {
		t.Fatal(err)
	}
	response := cliHandoffResponse{
		Stream: doubleStream, Room: doubleRoom, FromAgent: doubleFromAgent,
		ToAgent: doubleToAgent, CorrelationID: "handoff-fresh", CompletedType: "handoff.completed",
	}
	status, err := waitForCompletion(2*time.Second, response, acceptanceID)
	if err == nil || status != "" || !strings.Contains(err.Error(), "non-closed status") {
		t.Fatalf("status = %q, err = %v", status, err)
	}
}

func TestCompletionRejectsDuplicateOrContentBearingClosure(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra []string
		count int
		want  string
	}{
		{name: "duplicate", count: 2, want: "malformed declaration"},
		{name: "body", count: 1, extra: []string{"--body", "semantic"}, want: "semantic content"},
		{name: "artifact", count: 1, extra: []string{"--artifact-id", "artifact-1"}, want: "semantic content"},
	} {
		t.Run(test.name, func(t *testing.T) {
			shim, _ := installLogstreamDouble(t)
			acceptanceID, err := doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
			if err != nil {
				t.Fatal(err)
			}
			for range test.count {
				if _, err := doubleAppend(shim, doubleStream, "handoff.completed", "handoff-fresh", "applied", test.extra...); err != nil {
					t.Fatal(err)
				}
			}
			response := cliHandoffResponse{
				Stream: doubleStream, Room: doubleRoom, FromAgent: doubleFromAgent,
				ToAgent: doubleToAgent, CorrelationID: "handoff-fresh", CompletedType: "handoff.completed",
			}
			if _, err := waitForCompletion(2*time.Second, response, acceptanceID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDirectedHandoffRejectsBroadcastEvents(t *testing.T) {
	t.Run("acceptance", func(t *testing.T) {
		shim, _ := installLogstreamDouble(t)
		if _, err := doubleAppendRoute(shim, doubleStream, doubleRoom, "handoff.accepted",
			doubleFromAgent, "*", "handoff-fresh", "claimed"); err != nil {
			t.Fatal(err)
		}
		response := cliHandoffResponse{
			Stream: doubleStream, Room: doubleRoom, FromAgent: doubleFromAgent,
			ToAgent: doubleToAgent, CorrelationID: "handoff-fresh", AcceptedType: "handoff.accepted",
		}
		if _, err := waitForAcceptance(100*time.Millisecond, response); err == nil ||
			!strings.Contains(err.Error(), "mismatched") {
			t.Fatalf("err = %v, want mismatched declaration", err)
		}
	})

	t.Run("completion", func(t *testing.T) {
		shim, _ := installLogstreamDouble(t)
		acceptanceID, err := doubleAppend(shim, doubleStream, "handoff.accepted", "handoff-fresh", "claimed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doubleAppendRoute(shim, doubleStream, doubleRoom, "handoff.completed",
			doubleFromAgent, "*", "handoff-fresh", "applied"); err != nil {
			t.Fatal(err)
		}
		response := cliHandoffResponse{
			Stream: doubleStream, Room: doubleRoom, FromAgent: doubleFromAgent,
			ToAgent: doubleToAgent, CorrelationID: "handoff-fresh", CompletedType: "handoff.completed",
		}
		if _, err := waitForCompletion(time.Second, response, acceptanceID); err == nil ||
			!strings.Contains(err.Error(), "mismatched") {
			t.Fatalf("err = %v, want mismatched declaration", err)
		}
	})
}

func TestReturnedLogstreamRouteMustMatchExactly(t *testing.T) {
	response := cliHandoffResponse{
		Stream: doubleStream, Room: doubleRoom, FromAgent: doubleFromAgent,
		ToAgent: doubleToAgent, CorrelationID: "handoff-fresh",
	}
	base := logstreamEvent{
		ID: "event-1", Type: "handoff.completed", Stream: response.Stream, Room: response.Room,
		FromAgent: response.FromAgent, ToAgent: response.ToAgent, Correlation: response.CorrelationID,
	}
	tests := []struct {
		name   string
		mutate func(*logstreamEvent)
	}{
		{"type", func(e *logstreamEvent) { e.Type = "handoff.accepted" }},
		{"stream", func(e *logstreamEvent) { e.Stream = "project/other" }},
		{"room", func(e *logstreamEvent) { e.Room = "reviews" }},
		{"sender", func(e *logstreamEvent) { e.FromAgent = "delegate-other" }},
		{"recipient", func(e *logstreamEvent) { e.ToAgent = "other" }},
		{"broadcast recipient", func(e *logstreamEvent) { e.ToAgent = "*" }},
		{"correlation", func(e *logstreamEvent) { e.Correlation = "handoff-stale" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			test.mutate(&event)
			if exactLogstreamRoute(event, response, "handoff.completed") {
				t.Fatal("mismatched route accepted")
			}
		})
	}
}

func newHandoffStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/experimental/projects/p1/agent-handoffs" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "unconfined":true,
            "session":{"id":"s1"},
            "evidence":{"state":"available"},
            "stream":"` + doubleStream + `",
            "room":"` + doubleRoom + `",
            "from_agent":"` + doubleFromAgent + `",
            "to_agent":"` + doubleToAgent + `",
            "requested_correlation_id":"operator-route",
            "correlation_id":"handoff-fresh",
            "accepted_type":"handoff.accepted",
            "completed_type":"handoff.completed"
        }`))
	}))
}

func TestExperimentRequestFailsBeforeNetworkWithoutCapability(t *testing.T) {
	t.Setenv(experimentapi.CapabilityEnv, "")
	var response json.RawMessage
	err := experimentRequest("http://127.0.0.1:1", http.MethodGet, "/api/experimental/harnesses", nil, &response)
	if err == nil || !strings.Contains(err.Error(), "capability is unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestAcceptanceTimeoutLeavesVisibleSessionAndFails(t *testing.T) {
	installFakeMempalace(t, "", 2)
	response := cliHandoffResponse{
		Stream: "project/deck", Room: "implementation", FromAgent: "delegate-coder-claude",
		ToAgent: "source", CorrelationID: "handoff-fresh", AcceptedType: "handoff.accepted",
	}
	eventID, err := waitForAcceptance(time.Second, response)
	if eventID != "" || err == nil || !strings.Contains(err.Error(), "remains visible") {
		t.Fatalf("eventID = %q, err = %v", eventID, err)
	}
}

func installFakeMempalace(t *testing.T, output string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\nexit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "mempalace")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const (
	doubleStream    = "project/deck"
	doubleRoom      = "implementation"
	doubleFromAgent = "delegate-reviewer-claude"
	doubleToAgent   = "source"
)

// installLogstreamDouble puts a stateful mempalace Logstream double on PATH.
// It stores events in a JSONL file named by MEMPALACE_DOUBLE_STORE and
// implements the append/wait subset with the real CLI's filter semantics:
// exact route/correlation/status matching, to_agent also matching broadcasts,
// since-event-id as a strict append-order cursor, and exit 2 on wait timeout.
// The development image ships python3; tests append sequentially, so the
// read-modify-write store needs no writer lock.
func installLogstreamDouble(t *testing.T) (shimPath, storePath string) {
	t.Helper()
	dir := t.TempDir()
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	storePath = filepath.Join(dir, "logstream.jsonl")
	shimPath = filepath.Join(dir, "mempalace")
	if err := os.WriteFile(shimPath, []byte(logstreamDoubleScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MEMPALACE_DOUBLE_STORE", storePath)
	return shimPath, storePath
}

// doubleAppend appends one event through the double and returns its id.
func doubleAppend(shimPath, stream, eventType, correlationID, status string, extra ...string) (string, error) {
	return doubleAppendRoute(shimPath, stream, doubleRoom, eventType, doubleFromAgent,
		doubleToAgent, correlationID, status, extra...)
}

func doubleAppendRoute(shimPath, stream, room, eventType, fromAgent, toAgent, correlationID, status string, extra ...string) (string, error) {
	args := []string{"logstream", "append",
		"--type", eventType, "--stream", stream, "--room", room,
		"--from-agent", fromAgent, "--to-agent", toAgent,
		"--correlation-id", correlationID, "--status", status, "--json"}
	args = append(args, extra...)
	cmd := exec.Command(shimPath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	var event struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &event); err != nil || event.ID == "" {
		return "", fmt.Errorf("double append returned no event id: %s", output)
	}
	return event.ID, nil
}

const logstreamDoubleScript = `#!/usr/bin/env python3
import json, os, sys, time

STORE = os.environ["MEMPALACE_DOUBLE_STORE"]


def load():
    try:
        with open(STORE) as f:
            return [json.loads(line) for line in f if line.strip()]
    except FileNotFoundError:
        return []


def save(events):
    tmp = STORE + ".tmp"
    with open(tmp, "w") as f:
        for event in events:
            f.write(json.dumps(event) + "\n")
    os.replace(tmp, STORE)


def parse_flags(argv):
    flags = {}
    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg == "--json":
            flags["json"] = True
            i += 1
        elif arg.startswith("--"):
            flags[arg[2:]] = argv[i + 1]
            i += 2
        else:
            i += 1
    return flags


def matches(event, flags):
    for key in ("stream", "room", "type", "from-agent", "correlation-id", "status"):
        wanted = flags.get(key)
        if wanted is not None and event[key.replace("-", "_")] != wanted:
            return False
    wanted_to = flags.get("to-agent")
    if wanted_to is not None and event["to_agent"] not in (wanted_to, "*"):
        return False
    return True


def main():
    if len(sys.argv) < 3 or sys.argv[1] != "logstream":
        print("usage: mempalace logstream append|wait", file=sys.stderr)
        return 2
    action = sys.argv[2]
    flags = parse_flags(sys.argv[3:])
    if action == "append":
        events = load()
        event = {
            "id": "ev-%06d" % (len(events) + 1),
            "type": flags["type"],
            "stream": flags["stream"],
            "room": flags["room"],
            "from_agent": flags["from-agent"],
            "to_agent": flags["to-agent"],
            "correlation_id": flags["correlation-id"],
            "status": flags.get("status"),
            "body": flags.get("body", ""),
            "metadata": {},
            "artifact_ids": [flags["artifact-id"]] if "artifact-id" in flags else [],
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        events.append(event)
        save(events)
        print(json.dumps(event))
        return 0
    if action == "wait":
        timeout_ms = int(flags.get("timeout-ms", "60000"))
        deadline = time.monotonic() + timeout_ms / 1000.0
        while True:
            events = load()
            if flags.get("since-event-id") is not None:
                anchor = next(
                    (i for i, e in enumerate(events) if e["id"] == flags["since-event-id"]),
                    None,
                )
                if anchor is None:
                    print("unknown since-event-id", file=sys.stderr)
                    return 2
                events = events[anchor + 1 :]
            found = [e for e in events if matches(e, flags)]
            if found or time.monotonic() >= deadline:
                print(json.dumps({"events": found, "count": len(found), "timed_out": not found}))
                return 0 if found else 2
            time.sleep(0.025)
    print("usage: mempalace logstream append|wait", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
`

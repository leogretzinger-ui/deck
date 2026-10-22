package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	experimentapi "github.com/noisefloor-oss/deck/internal/experiment"
)

const defaultControlURL = "http://127.0.0.1:8837"

func runHarnesses(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeln(stderr, "usage: noise deck harnesses <list|show> [options]")
		return 2
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("noise deck harnesses list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		server := flags.String("server", controlURLDefault(), "Deck control URL (loopback only)")
		_ = flags.Bool("json", false, "emit JSON (the experiment always emits structured JSON)")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return 2
		}
		return getExperimentJSON(*server, "/api/experimental/harnesses", stdout, stderr)
	case "show":
		flags := flag.NewFlagSet("noise deck harnesses show", flag.ContinueOnError)
		flags.SetOutput(stderr)
		server := flags.String("server", controlURLDefault(), "Deck control URL (loopback only)")
		_ = flags.Bool("json", false, "emit JSON (the experiment always emits structured JSON)")
		if len(args) < 2 {
			writeln(stderr, "noise deck harnesses show: harness id is required")
			return 2
		}
		providerID := args[1]
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return 2
		}
		return getExperimentJSON(*server, "/api/experimental/harnesses/"+url.PathEscape(providerID), stdout, stderr)
	default:
		writef(stderr, "noise deck harnesses: unknown command %q\n", args[0])
		return 2
	}
}

type cliHandoffRequest struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Role          string `json:"role"`
	TaskID        string `json:"task_id"`
	CorrelationID string `json:"correlation_id"`
}

type cliHandoffResponse struct {
	Unconfined             bool            `json:"unconfined"`
	Session                json.RawMessage `json:"session"`
	Evidence               json.RawMessage `json:"evidence"`
	Stream                 string          `json:"stream"`
	Room                   string          `json:"room"`
	FromAgent              string          `json:"from_agent"`
	ToAgent                string          `json:"to_agent"`
	RequestedCorrelationID string          `json:"requested_correlation_id"`
	CorrelationID          string          `json:"correlation_id"`
	AcceptedType           string          `json:"accepted_type"`
	CompletedType          string          `json:"completed_type"`
}

type cliHandoffResult struct {
	Unconfined bool            `json:"unconfined"`
	Accepted   bool            `json:"accepted"`
	Completed  bool            `json:"completed"`
	Session    json.RawMessage `json:"session"`
	Evidence   json.RawMessage `json:"evidence"`
	Acceptance struct {
		Stream                 string `json:"stream"`
		Room                   string `json:"room"`
		FromAgent              string `json:"from_agent"`
		ToAgent                string `json:"to_agent"`
		RequestedCorrelationID string `json:"requested_correlation_id"`
		CorrelationID          string `json:"correlation_id"`
		Type                   string `json:"type"`
	} `json:"acceptance"`
	Completion struct {
		Type   string `json:"type"`
		Status string `json:"status,omitempty"`
	} `json:"completion"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

func runAgentHandoff(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("noise deck agent-handoff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", controlURLDefault(), "Deck control URL (loopback only)")
	projectID := flags.String("project-id", os.Getenv("NOISEDECK_PROJECT_ID"), "existing Deck project id")
	providerID := flags.String("provider", "", "target provider id")
	model := flags.String("model", "", "target model reported by the provider")
	role := flags.String("role", "", "opaque lifecycle role id (for example reviewer or review.formal)")
	taskID := flags.String("task", "", "canonical noise task id")
	correlationID := flags.String("correlation-id", "", "new opaque Logstream correlation id")
	timeout := flags.Duration("timeout", 5*time.Minute, "acceptance wait (maximum 5m)")
	completionTimeout := flags.Duration("completion-timeout", defaultCompletionTimeout,
		"delegated task completion wait (default 30m, maximum 2h)")
	_ = flags.Bool("json", false, "emit JSON (the experiment always emits structured JSON)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *projectID == "" || *providerID == "" || *model == "" ||
		*role == "" || *taskID == "" || *correlationID == "" {
		writeln(stderr, "noise deck agent-handoff: --project-id, --provider, --model, --role, --task, and --correlation-id are required")
		return 2
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		writeln(stderr, "noise deck agent-handoff: --timeout must be greater than zero and at most 5m")
		return 2
	}
	if *completionTimeout <= 0 || *completionTimeout > maxCompletionTimeout {
		writeln(stderr, "noise deck agent-handoff: --completion-timeout must be greater than zero and at most 2h")
		return 2
	}

	request := cliHandoffRequest{Provider: *providerID, Model: *model, Role: *role,
		TaskID: *taskID, CorrelationID: *correlationID}
	var response cliHandoffResponse
	path := "/api/experimental/projects/" + url.PathEscape(*projectID) + "/agent-handoffs"
	if err := experimentRequest(*server, http.MethodPost, path, request, &response); err != nil {
		writef(stderr, "noise deck agent-handoff: %v\n", err)
		return 1
	}

	result := cliHandoffResult{Unconfined: true, Session: response.Session, Evidence: response.Evidence}
	result.Acceptance.Stream = response.Stream
	result.Acceptance.Room = response.Room
	result.Acceptance.FromAgent = response.FromAgent
	result.Acceptance.ToAgent = response.ToAgent
	result.Acceptance.RequestedCorrelationID = response.RequestedCorrelationID
	result.Acceptance.CorrelationID = response.CorrelationID
	result.Acceptance.Type = response.AcceptedType
	result.Completion.Type = response.CompletedType

	acceptanceEventID, err := waitForAcceptance(*timeout, response)
	result.Accepted = err == nil
	if err != nil {
		result.Diagnostic = err.Error()
	} else {
		var status string
		status, err = waitForCompletion(*completionTimeout, response, acceptanceEventID)
		if err != nil {
			result.Diagnostic = err.Error()
		} else {
			result.Completed = true
			result.Completion.Status = status
			if status == completionStatusFailed {
				result.Diagnostic = "delegated receiver declared the task failed"
			}
		}
	}
	if encodeErr := writeJSON(stdout, result); encodeErr != nil {
		writef(stderr, "noise deck agent-handoff: write result: %v\n", encodeErr)
		return 1
	}
	if !result.Accepted || !result.Completed || result.Completion.Status != completionStatusComplete {
		return 1
	}
	return 0
}

func waitForAcceptance(timeout time.Duration, response cliHandoffResponse) (string, error) {
	timeoutMS := strconv.FormatInt(timeout.Milliseconds(), 10)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mempalace", "logstream", "wait",
		"--stream", response.Stream, "--room", response.Room,
		"--type", response.AcceptedType, "--from-agent", response.FromAgent,
		"--to-agent", response.ToAgent, "--correlation-id", response.CorrelationID,
		"--status", "claimed", "--timeout-ms", timeoutMS, "--json")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return "", errors.New("delegated session remains visible but did not declare acceptance before timeout")
		}
		return "", errors.New("acceptance wait failed")
	}
	var result logstreamWaitResult
	if json.Unmarshal(output.Bytes(), &result) != nil || result.TimedOut || result.Count < 1 ||
		len(result.Events) == 0 || result.Events[0].ID == "" {
		return "", errors.New("acceptance wait returned no matching receiver declaration")
	}
	if !exactLogstreamRoute(result.Events[0], response, response.AcceptedType) {
		return "", errors.New("acceptance wait returned a mismatched receiver declaration")
	}
	return result.Events[0].ID, nil
}

// The Logstream status vocabulary has no literal "complete" value: applied is
// its completed-successfully closure and failed its unsuccessful one, so those
// two statuses form this experiment's closed completion set.
const (
	completionStatusComplete = "applied"
	completionStatusFailed   = "failed"
)

const (
	defaultCompletionTimeout = 30 * time.Minute
	maxCompletionTimeout     = 2 * time.Hour
	// One mempalace logstream wait invocation blocks at most five minutes, so
	// a longer user-selected deadline is waited in bounded chunks.
	logstreamWaitChunk = 5 * time.Minute
)

var errCompletionTimeout = errors.New("delegated session remains visible but did not declare completion before timeout")

type logstreamEvent struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Stream      string         `json:"stream"`
	Room        string         `json:"room"`
	FromAgent   string         `json:"from_agent"`
	ToAgent     string         `json:"to_agent"`
	Correlation string         `json:"correlation_id"`
	Status      string         `json:"status"`
	Body        string         `json:"body"`
	Metadata    map[string]any `json:"metadata"`
	ArtifactIDs []string       `json:"artifact_ids"`
}

type logstreamWaitResult struct {
	Count    int              `json:"count"`
	TimedOut bool             `json:"timed_out"`
	Events   []logstreamEvent `json:"events"`
}

func exactLogstreamRoute(event logstreamEvent, response cliHandoffResponse, eventType string) bool {
	return event.Type == eventType && event.Stream == response.Stream && event.Room == response.Room &&
		event.FromAgent == response.FromAgent && event.ToAgent == response.ToAgent &&
		event.Correlation == response.CorrelationID
}

// waitForCompletion blocks until the receiver appends the completion event on
// the same fresh route and correlation as its acceptance, strictly after that
// acceptance, and returns the observed closed status. It never treats
// terminal output, process exit, or task state as completion.
func waitForCompletion(timeout time.Duration, response cliHandoffResponse, acceptanceEventID string) (string, error) {
	if response.CompletedType == "" {
		return "", errors.New("handoff response did not name a completion event type")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", errCompletionTimeout
		}
		chunk := remaining
		if chunk > logstreamWaitChunk {
			chunk = logstreamWaitChunk
		}
		args := []string{"logstream", "wait",
			"--stream", response.Stream, "--room", response.Room,
			"--type", response.CompletedType, "--from-agent", response.FromAgent,
			"--to-agent", response.ToAgent, "--correlation-id", response.CorrelationID,
			"--timeout-ms", strconv.FormatInt(chunk.Milliseconds(), 10), "--json"}
		if acceptanceEventID != "" {
			// The closure must postdate the acceptance it closes; the event id
			// is the Logstream append-order cursor.
			args = append(args, "--since-event-id", acceptanceEventID)
		}
		var output bytes.Buffer
		cmd := exec.CommandContext(ctx, "mempalace", args...)
		cmd.Stdout = &output
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if err != nil {
			if ctx.Err() != nil {
				return "", errCompletionTimeout
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
				continue
			}
			return "", errors.New("completion wait failed")
		}
		var result logstreamWaitResult
		if json.Unmarshal(output.Bytes(), &result) != nil || result.Count != 1 || len(result.Events) != 1 {
			return "", errors.New("completion wait returned a malformed declaration")
		}
		if !exactLogstreamRoute(result.Events[0], response, response.CompletedType) {
			return "", errors.New("completion wait returned a mismatched receiver declaration")
		}
		// The log is append-only and the receiver is told to close exactly
		// once, so the first closure is authoritative.
		status := result.Events[0].Status
		if result.Events[0].Body != "" || len(result.Events[0].Metadata) != 0 ||
			len(result.Events[0].ArtifactIDs) != 0 {
			return "", errors.New("completion event carried semantic content")
		}
		if status != completionStatusComplete && status != completionStatusFailed {
			return "", fmt.Errorf("completion event carried non-closed status %q", status)
		}
		return status, nil
	}
}

func getExperimentJSON(server, path string, stdout, stderr io.Writer) int {
	var response json.RawMessage
	if err := experimentRequest(server, http.MethodGet, path, nil, &response); err != nil {
		writef(stderr, "noise deck harnesses: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(response, '\n')); err != nil {
		writef(stderr, "noise deck harnesses: write result: %v\n", err)
		return 1
	}
	return 0
}

func experimentRequest(server, method, path string, request, response any) error {
	base, err := validateControlURL(server)
	if err != nil {
		return err
	}
	var body io.Reader
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	capability := strings.TrimSpace(os.Getenv(experimentapi.CapabilityEnv))
	if capability == "" {
		return errors.New("unconfined Deck experiment capability is unavailable")
	}
	req.Header.Set(experimentapi.CapabilityHeader, capability)
	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("unconfined Deck control request failed")
	}
	defer func() { _ = res.Body.Close() }()
	decoder := json.NewDecoder(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if decoder.Decode(&problem) == nil && problem.Error.Message != "" {
			return errors.New(problem.Error.Message)
		}
		return fmt.Errorf("unconfined Deck control request returned HTTP %d", res.StatusCode)
	}
	if err := decoder.Decode(response); err != nil {
		return errors.New("unconfined Deck control response was unreadable")
	}
	return nil
}

func validateControlURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("deck control URL must be a loopback http origin")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("deck control URL must be a loopback http origin")
	}
	return strings.TrimRight(raw, "/"), nil
}

func controlURLDefault() string {
	if configured := strings.TrimSpace(os.Getenv("NOISEDECK_CONTROL_URL")); configured != "" {
		return configured
	}
	return defaultControlURL
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

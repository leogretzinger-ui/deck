package capacity

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/provider"
)

var claudeUsageLine = regexp.MustCompile(`^(.+?):\s*([0-9]+(?:\.[0-9]+)?)% used\s*[·-]\s*resets\s+(.+)$`)

type claudeCommandResult struct {
	Result string `json:"result"`
}

func (c *Collector) collectClaude(ctx context.Context, executable string, observedAt time.Time) Evidence {
	evidence := Evidence{
		Provider: provider.Claude, Source: "claude-slash-command",
		ObservedAt: observedAt, Models: []Model{}, Windows: []Window{},
	}
	modelOutput, modelErr := c.run(ctx, executable, "--print", "--output-format", "json",
		"--tools", "", "--no-session-persistence", "/model")
	if modelErr == nil {
		evidence.Models = parseClaudeModels(modelOutput)
	}
	usageOutput, usageErr := c.run(ctx, executable, "--print", "--output-format", "json",
		"--tools", "", "--no-session-persistence", "/usage")
	if usageErr == nil {
		evidence.Windows = parseClaudeUsage(usageOutput, observedAt)
	}
	switch {
	case modelErr != nil && usageErr != nil:
		evidence.Diagnostic = "Claude model and usage commands failed"
	case modelErr != nil:
		evidence.Diagnostic = commandDiagnostic("Claude model", modelErr)
	case usageErr != nil:
		evidence.Diagnostic = commandDiagnostic("Claude usage", usageErr)
	case len(evidence.Models) == 0 || len(evidence.Windows) == 0:
		evidence.Diagnostic = "Claude returned no recognized model or usage fields"
	}
	return partialOrAvailable(evidence)
}

func claudeResult(raw []byte) string {
	var response claudeCommandResult
	if json.Unmarshal(raw, &response) != nil {
		return ""
	}
	return response.Result
}

func parseClaudeModels(raw []byte) []Model {
	result := claudeResult(raw)
	marker := "Available:"
	index := strings.Index(result, marker)
	if index < 0 {
		return []Model{}
	}
	available := strings.TrimSpace(result[index+len(marker):])
	available = strings.TrimSuffix(available, ".")
	available = strings.TrimSuffix(available, "or a full model ID")
	available = strings.TrimSpace(strings.TrimSuffix(available, ","))
	available = strings.ReplaceAll(available, " or ", ", ")
	models := make([]Model, 0)
	seen := make(map[string]struct{})
	for _, item := range strings.Split(available, ",") {
		id := strings.TrimSpace(item)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, Model{ID: id})
	}
	return models
}

func parseClaudeUsage(raw []byte, observedAt time.Time) []Window {
	result := claudeResult(raw)
	windows := make([]Window, 0, 3)
	for _, line := range strings.Split(result, "\n") {
		match := claudeUsageLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		used, err := strconv.ParseFloat(match[2], 64)
		if err != nil || used < 0 || used > 100 {
			continue
		}
		label := strings.TrimSpace(match[1])
		window := Window{Name: windowName(label), Scope: windowScope(label), UsedPercent: used}
		if reset, ok := parseClaudeReset(match[3], observedAt); ok {
			window.ResetsAt = timePointer(reset)
		}
		windows = append(windows, window)
	}
	return windows
}

func windowName(label string) string {
	lower := strings.ToLower(label)
	switch {
	case strings.Contains(lower, "session"):
		return "session"
	case strings.Contains(lower, "week"):
		return "week"
	default:
		return lower
	}
}

func windowScope(label string) string {
	open := strings.LastIndex(label, "(")
	close := strings.LastIndex(label, ")")
	if open < 0 || close <= open {
		return "account"
	}
	scope := strings.ToLower(strings.TrimSpace(label[open+1 : close]))
	if scope == "all models" {
		return "all_models"
	}
	return "model:" + scope
}

func parseClaudeReset(value string, observedAt time.Time) (time.Time, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "(UTC)"))
	value = strings.TrimSpace(value)
	for _, layout := range []string{"Jan 2, 2006 3:04pm", "Jan 2, 2006 3pm"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), true
		}
	}
	for _, layout := range []string{"Jan 2, 3:04pm", "Jan 2, 3pm"} {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err != nil {
			continue
		}
		parsed = time.Date(observedAt.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
		if parsed.Before(observedAt.Add(-24 * time.Hour)) {
			parsed = parsed.AddDate(1, 0, 0)
		}
		return parsed, true
	}
	return time.Time{}, false
}

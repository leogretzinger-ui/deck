package capacity

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/noisefloor-oss/deck/internal/provider"
)

type codexRPCResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
}

type codexModelResponse struct {
	Data []struct {
		ID                        string `json:"id"`
		DisplayName               string `json:"displayName"`
		Default                   bool   `json:"isDefault"`
		SupportedReasoningEfforts []struct {
			Effort string `json:"reasoningEffort"`
		} `json:"supportedReasoningEfforts"`
	} `json:"data"`
}

type codexWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowDurationMin *int64  `json:"windowDurationMins"`
	ResetsAt          *int64  `json:"resetsAt"`
}

type codexSnapshot struct {
	LimitID   *string      `json:"limitId"`
	LimitName *string      `json:"limitName"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexUsageResponse struct {
	RateLimits          codexSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]codexSnapshot `json:"rateLimitsByLimitId"`
}

func (c *Collector) collectCodex(ctx context.Context, executable string, observedAt time.Time) Evidence {
	evidence := Evidence{
		Provider: provider.Codex, Source: "codex-app-server",
		ObservedAt: observedAt, Models: []Model{}, Windows: []Window{},
	}
	cmd := exec.CommandContext(ctx, executable, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		evidence.Diagnostic = "Codex app-server could not start"
		return partialOrAvailable(evidence)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		evidence.Diagnostic = "Codex app-server could not start"
		return partialOrAvailable(evidence)
	}
	stderr := &limitedBuffer{limit: maxCommandOutput}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		evidence.Diagnostic = "Codex app-server could not start"
		return partialOrAvailable(evidence)
	}
	encoder := json.NewEncoder(stdin)
	initialize := map[string]any{"method": "initialize", "id": 1, "params": map[string]any{"clientInfo": map[string]any{"name": "noisedeck-capacity-spike", "title": "Deck capacity spike", "version": "0.1.0"}}}
	requests := []any{
		map[string]any{"method": "initialized", "params": map[string]any{}},
		map[string]any{"method": "account/rateLimits/read", "id": 2, "params": map[string]any{}},
		map[string]any{"method": "model/list", "id": 3, "params": map[string]any{"includeHidden": false}},
	}
	if err := encoder.Encode(initialize); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		evidence.Diagnostic = "Codex app-server request failed"
		return partialOrAvailable(evidence)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxCommandOutput)
	initialized := false
	requestFailed := false
	gotUsage := false
	gotModels := false
	for scanner.Scan() {
		var response codexRPCResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}
		switch response.ID {
		case 1:
			if initialized {
				continue
			}
			initialized = true
			for _, request := range requests {
				if err := encoder.Encode(request); err != nil {
					evidence.Diagnostic = "Codex app-server request failed"
					requestFailed = true
					break
				}
			}
		case 2:
			var usage codexUsageResponse
			if json.Unmarshal(response.Result, &usage) == nil {
				evidence.Windows = codexWindows(usage)
			}
			gotUsage = true
		case 3:
			var models codexModelResponse
			if json.Unmarshal(response.Result, &models) == nil {
				for _, item := range models.Data {
					efforts := make([]string, 0, len(item.SupportedReasoningEfforts))
					for _, effort := range item.SupportedReasoningEfforts {
						efforts = append(efforts, effort.Effort)
					}
					evidence.Models = append(evidence.Models, Model{ID: item.ID, DisplayName: item.DisplayName, Default: item.Default, ReasoningEfforts: efforts})
				}
			}
			gotModels = true
		}
		if requestFailed || gotUsage && gotModels {
			break
		}
	}
	_ = stdin.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if len(evidence.Models) == 0 || len(evidence.Windows) == 0 {
		evidence.Diagnostic = "Codex returned no recognized model or usage fields"
	}
	return partialOrAvailable(evidence)
}

func codexWindows(response codexUsageResponse) []Window {
	windows := make([]Window, 0, 6)
	appendSnapshot := func(scope string, snapshot codexSnapshot) {
		if snapshot.Primary != nil {
			windows = append(windows, normalizedCodexWindow("primary", scope, *snapshot.Primary))
		}
		if snapshot.Secondary != nil {
			windows = append(windows, normalizedCodexWindow("secondary", scope, *snapshot.Secondary))
		}
	}
	appendSnapshot("account", response.RateLimits)
	keys := make([]string, 0, len(response.RateLimitsByLimitID))
	for key := range response.RateLimitsByLimitID {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		appendSnapshot("limit:"+key, response.RateLimitsByLimitID[key])
	}
	return windows
}

func normalizedCodexWindow(name, scope string, source codexWindow) Window {
	window := Window{Name: name, Scope: scope, UsedPercent: source.UsedPercent, DurationMins: source.WindowDurationMin}
	if source.ResetsAt != nil {
		window.ResetsAt = timePointer(time.Unix(*source.ResetsAt, 0).UTC())
	}
	return window
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

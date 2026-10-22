package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// EnsureTask uses the engine's replay-safe plan receipt. Repeating after a
// crash between task creation and the caller's receipt never creates a second
// task or claims a similarly named human task.
func (e Engine) EnsureTask(ctx context.Context, key, title, description string) (int64, error) {
	plan := map[string]any{
		"schemaVersion": 1, "planId": key, "tag": e.Tag,
		"milestones": []any{}, "assignments": []any{},
		"tasks": []any{map[string]any{
			"key": "repair", "title": title, "description": description,
			"details":  "Repair through isolated CI heal execution and the standard review/merge gate.",
			"priority": "high", "dependencies": []any{},
		}},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "ciheal-plan-*.json")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	validated, err := e.run(ctx, "plan", "validate", "--offline", "--json", f.Name())
	if err != nil {
		return 0, err
	}
	var validation struct {
		Digest string `json:"planDigest"`
	}
	if err := json.Unmarshal(validated, &validation); err != nil || validation.Digest == "" {
		return 0, fmt.Errorf("engine plan validation returned no digest")
	}
	applied, err := e.run(ctx, "plan", "apply", "--digest", validation.Digest, "--request-id", key, "--json", f.Name())
	if err != nil {
		return 0, err
	}
	var result struct {
		Receipt struct {
			Mappings []struct {
				Key string `json:"key"`
				ID  string `json:"id"`
			} `json:"taskMappings"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal(applied, &result); err != nil || len(result.Receipt.Mappings) != 1 || result.Receipt.Mappings[0].Key != "repair" {
		return 0, fmt.Errorf("engine plan apply returned an invalid task mapping")
	}
	id, err := strconv.ParseInt(result.Receipt.Mappings[0].ID, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("engine plan apply returned an invalid task id")
	}
	return id, nil
}

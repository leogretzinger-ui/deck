package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

type liveFeedResult struct {
	Events []struct {
		Seq int64 `json:"seq"`
	} `json:"events"`
	OldestRetainedSeq int64 `json:"oldestRetainedSeq"`
	Truncated         bool  `json:"truncated"`
}

func readLiveFeed(ctx context.Context, executable string, root *taskRoot, since int64) (liveFeedResult, error) {
	if since < 0 {
		return liveFeedResult{}, errors.New("taskmaster: invalid Tasks feed cursor")
	}
	raw, err := runTaskEngine(ctx, executable, root, "live", "feed", "--since", strconv.FormatInt(since, 10), "--json")
	if err != nil {
		return liveFeedResult{}, err
	}
	var shape map[string]json.RawMessage
	if err := decodeOneJSON(raw, &shape); err != nil || len(shape) != 3 ||
		shape["events"] == nil || shape["oldestRetainedSeq"] == nil || shape["truncated"] == nil {
		return liveFeedResult{}, errors.New("taskmaster: Tasks feed returned an unsupported shape")
	}
	var result liveFeedResult
	if err := json.Unmarshal(raw, &result); err != nil || result.Events == nil || result.OldestRetainedSeq < 0 {
		return liveFeedResult{}, errors.New("taskmaster: Tasks feed returned an invalid result")
	}
	last := since
	for _, event := range result.Events {
		if event.Seq <= last {
			return liveFeedResult{}, errors.New("taskmaster: Tasks feed returned non-increasing events")
		}
		last = event.Seq
	}
	return result, nil
}

func liveFeedCursor(feed liveFeedResult, fallback int64) int64 {
	if len(feed.Events) == 0 {
		return fallback
	}
	return feed.Events[len(feed.Events)-1].Seq
}

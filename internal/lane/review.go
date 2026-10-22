package lane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GH runs the GitHub CLI in a directory and returns its standard output.
type GH func(ctx context.Context, dir string, args ...string) ([]byte, error)

const (
	ghTimeout   = 30 * time.Second
	ghMaxOutput = 4 << 20
)

// execGH uses the GitHub CLI's own sign-in. Standard error is dropped: it can
// echo where credentials live.
func execGH(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{buf: &out, left: ghMaxOutput}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr list failed: %w", err)
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	buf  *bytes.Buffer
	left int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		return 0, errors.New("gh output exceeded its bound")
	}
	w.left -= len(p)
	return w.buf.Write(p)
}

// ReviewState is what Deck can tell about the review of a lane's pull request.
type ReviewState struct {
	Requested bool `json:"requested"`
	// Seen is set when the lane's pull request has a comment or review that
	// arrived after the review was requested.
	Seen        bool `json:"seen"`
	PullRequest int  `json:"pull_request,omitempty"`
}

// RequestReview records which agent was asked to review the lane's pull
// request. The agent must differ from the one that wrote the change.
func (s *Service) RequestReview(parent context.Context, projectID, tag string, taskID int64, agent string) (*View, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" || taskID <= 0 || strings.TrimSpace(tag) == "" {
		return nil, fmt.Errorf("%w: a tag, a task and an agent are required", ErrInvalid)
	}
	ctx := context.WithoutCancel(parent)
	unlock := s.lock(projectID, tag, taskID)
	defer unlock()
	l, err := s.cfg.Store.Get(ctx, projectID, tag, taskID)
	if err != nil {
		return nil, err
	}
	if l.Phase != PhaseOpen {
		return nil, ErrNotReady
	}
	if agent == l.Agent {
		return nil, ErrSameAgent
	}
	l.ReviewAgent, l.ReviewRequestedAt = agent, s.cfg.Now()
	if err := s.cfg.Store.Save(ctx, l); err != nil {
		return nil, err
	}
	return viewOf(l), nil
}

type pullRequest struct {
	Number   int `json:"number"`
	Comments []struct {
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
	Reviews []struct {
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
}

// Review reads the lane's pull request through the GitHub CLI and reports
// whether anything was posted on it since the review was requested. The
// check names no author: the comment comes from whichever account the CLI is
// signed in as.
func (s *Service) Review(ctx context.Context, projectID, tag string, taskID int64) (*ReviewState, error) {
	p, err := s.cfg.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	l, err := s.cfg.Store.Get(ctx, projectID, tag, taskID)
	if err != nil {
		return nil, err
	}
	state := &ReviewState{Requested: !l.ReviewRequestedAt.IsZero()}
	if !state.Requested || l.Phase != PhaseOpen {
		return state, nil
	}
	raw, err := s.cfg.GH(ctx, p.CanonicalPath, "pr", "list", "--head", l.Branch, "--state", "all",
		"--json", "number,comments,reviews", "--limit", "1")
	if err != nil {
		return nil, err
	}
	var prs []pullRequest
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list returned unreadable JSON: %w", err)
	}
	if len(prs) == 0 {
		return state, nil
	}
	state.PullRequest = prs[0].Number
	for _, c := range prs[0].Comments {
		if c.CreatedAt.After(l.ReviewRequestedAt) {
			state.Seen = true
		}
	}
	for _, r := range prs[0].Reviews {
		if r.SubmittedAt.After(l.ReviewRequestedAt) {
			state.Seen = true
		}
	}
	return state, nil
}

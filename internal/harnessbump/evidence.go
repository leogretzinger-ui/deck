package harnessbump

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// BumpWorkflowPath is the trusted workflow whose runs may vouch for a head.
const BumpWorkflowPath = ".github/workflows/harness-bump.yaml" // #nosec G101 -- a workflow file path, not a credential.

const probeGateVersionFile = "probe-gate-version"
const probeGateVersion = "2\n"

// allProbeGateVersion marks a combined-batch gate that also ran every
// updated CLI's native a2a receive probe inside the built image.
const allProbeGateVersion = "4\n"

// EvidenceArtifact names the artifact a bump run uploads for a harness
// and version: a file `tip` holding the SHA it pushed after its gates.
func EvidenceArtifact(harness, version string) string {
	return "harness-bump-evidence-" + harness + "-" + version
}

// RunEvidence returns an Evidence that reads the bump workflow's own
// artifacts through gh. An artifact counts only when its run is the
// trusted workflow file on main, started by the schedule or by hand,
// completed, and its tip is the head in question. The artifact must carry
// the marker written after a fail-closed probe; older producers could hide
// a failed probe behind tee. A sibling harness or a later dispatch failure
// does not invalidate this harness's gates. A run of any other workflow,
// or on any other branch, vouches for nothing.
func RunEvidence(repo string) Evidence {
	return func(ctx context.Context, h Harness, version, head string) (bool, error) {
		name := EvidenceArtifact(h.Name, version)
		raw, err := ExecGH(ctx, "api", "repos/"+repo+"/actions/artifacts?name="+name+"&per_page=100")
		if err != nil {
			return false, err
		}
		var page struct {
			Artifacts []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Expired     bool   `json:"expired"`
				WorkflowRun struct {
					ID         int64  `json:"id"`
					HeadBranch string `json:"head_branch"`
				} `json:"workflow_run"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return false, fmt.Errorf("harnessbump: artifacts of %s: %w", name, err)
		}
		for _, a := range page.Artifacts {
			if a.Expired || a.Name != name || a.WorkflowRun.HeadBranch != "main" {
				continue
			}
			runRaw, err := ExecGH(ctx, "api", "repos/"+repo+"/actions/runs/"+strconv.FormatInt(a.WorkflowRun.ID, 10))
			if err != nil {
				return false, err
			}
			var run struct {
				Path       string `json:"path"`
				Event      string `json:"event"`
				Status     string `json:"status"`
				HeadBranch string `json:"head_branch"`
			}
			if err := json.Unmarshal(runRaw, &run); err != nil {
				return false, fmt.Errorf("harnessbump: run %d: %w", a.WorkflowRun.ID, err)
			}
			if run.Path != BumpWorkflowPath || run.HeadBranch != "main" || run.Status != "completed" || (run.Event != "schedule" && run.Event != "workflow_dispatch") {
				continue
			}
			marker := probeGateVersion
			if h.Name == "all" {
				marker = allProbeGateVersion
			}
			tip, err := artifactTip(ctx, repo, a.ID, marker)
			if err != nil {
				return false, err
			}
			if tip != "" && tip == head {
				return true, nil
			}
		}
		return false, nil
	}
}

// artifactTip downloads the artifact and reads its `tip` file.
func artifactTip(ctx context.Context, repo string, id int64, markers ...string) (string, error) {
	raw, err := ExecGH(ctx, "api", "repos/"+repo+"/actions/artifacts/"+strconv.FormatInt(id, 10)+"/zip")
	if err != nil {
		return "", err
	}
	marker := probeGateVersion
	if len(markers) > 0 {
		marker = markers[0]
	}
	return gatedTipFromZipVersion(raw, marker)
}

// Missing or unsupported probe markers are old evidence, not API errors:
// skip them so a newer run can vouch for the same tip.
func gatedTipFromZip(raw []byte) (string, error) {
	return gatedTipFromZipVersion(raw, probeGateVersion)
}

func gatedTipFromZipVersion(raw []byte, marker string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("harnessbump: evidence artifact: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != probeGateVersionFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(len(marker)+1)))
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		if string(b) != marker {
			return "", nil
		}
		return TipFromZip(raw)
	}
	return "", nil
}

// TipFromZip reads the `tip` entry of an evidence artifact.
func TipFromZip(raw []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("harnessbump: evidence artifact: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "tip" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, 128))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", fmt.Errorf("harnessbump: evidence artifact carries no tip")
}

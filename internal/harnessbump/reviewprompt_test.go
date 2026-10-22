package harnessbump

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReviewPromptBindsImmutableDiff(t *testing.T) {
	diff := []byte("diff --git a/Dockerfile b/Dockerfile\n-old\n+new\n")
	prompt := []byte("trusted instructions\n## Diff\n\n```diff\ndiff --git a/Dockerfile b/Dockerfile\n-old\n+new\n```\n")
	if err := VerifyReviewPrompt(prompt, diff); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReviewPrompt(bytes.TrimRight(prompt, "\n"), diff); err != nil {
		t.Fatal("shell command substitution removed the final newline:", err)
	}
	// curl writes body + newline + HTTP status; extracting the status leaves
	// the body's final newline intact. The renderer adds its own newline.
	rendered := bytes.Replace(prompt, []byte("+new\n```"), []byte("+new\n\n```"), 1)
	if err := VerifyReviewPrompt(rendered, diff); err != nil {
		t.Fatal("real dispatcher preserves the HTTP diff body newline:", err)
	}
	for _, changed := range [][]byte{[]byte("next head\n"), nil, append(diff, []byte("another file")...)} {
		if VerifyReviewPrompt(prompt, changed) == nil || VerifyReviewPrompt(rendered, changed) == nil {
			t.Fatal("accepted a raced head or base")
		}
	}
	if VerifyReviewPrompt(append(prompt, []byte("extra diff")...), diff) == nil {
		t.Fatal("accepted bytes after the bound diff")
	}
	if VerifyReviewPrompt(bytes.Replace(rendered, []byte("+new"), []byte("+different"), 1), diff) == nil {
		t.Fatal("accepted changed content in the real renderer shape")
	}
}

func TestBoundReviewPromptProvidesIdentityAndRepairEvidence(t *testing.T) {
	diff := []byte("net documentation diff\n")
	prompt := []byte("tool-owned review instructions\n## Diff\n\n```diff\nnet documentation diff\n```\n")
	snapshot := ReviewSnapshot{Repository: "acme/deck", PullRequest: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40)}
	evidence := []byte(`{"repair_diff":"-func  Repair()\n+func Repair()","check":"success"}`)
	bound, err := BoundReviewPrompt(prompt, diff, evidence, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf("%s PR #%d", snapshot.Repository, snapshot.PullRequest), "Reviewed head: " + snapshot.Head, "Base: " + snapshot.Base, "UNTRUSTED DATA", "func  Repair()", string(prompt)} {
		if !bytes.Contains(bound, []byte(want)) {
			t.Fatalf("review packet lost %q", want)
		}
	}
	for _, bad := range []ReviewSnapshot{{}, {Repository: "acme/deck\nwrong", PullRequest: 7, Head: snapshot.Head, Base: snapshot.Base}, {Repository: "acme/deck", PullRequest: 7, Head: "short", Base: snapshot.Base}} {
		if _, err := BoundReviewPrompt(prompt, diff, evidence, bad); err == nil {
			t.Fatal("accepted missing/unsafe identity")
		}
	}
	for _, bad := range [][]byte{[]byte("not JSON"), bytes.Repeat([]byte(" "), MaxReviewEvidenceBytes+1)} {
		if _, err := BoundReviewPrompt(prompt, diff, bad, snapshot); err == nil {
			t.Fatal("accepted malformed/oversized evidence")
		}
	}
	if _, err := BoundReviewPrompt(prompt, []byte("raced diff"), evidence, snapshot); err == nil {
		t.Fatal("accepted diff from another snapshot")
	}
}

func TestVerdictMustNameTheReviewedSnapshot(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	body := "✅ VERDICT: APPROVE\n\nReviewed head: " + head + "\nBase: " + base
	for _, fixture := range []struct {
		name, body string
		valid      bool
	}{
		{"bound", body, true},
		{"observed unbound approval", "✅ VERDICT: APPROVE\nOnly the documentation was reviewed", false},
		{"wrong head", strings.ReplaceAll(body, head, base), false},
		{"wrong base", strings.ReplaceAll(body, base, head), false},
		{"missing base", strings.Split(body, "\nBase:")[0], false},
		{"indented head", strings.Replace(body, "\nReviewed head:", "\n Reviewed head:", 1), false},
		{"tabbed head", strings.Replace(body, "\nReviewed head:", "\n\tReviewed head:", 1), false},
		{"space suffixed head", strings.Replace(body, "\nBase:", " \nBase:", 1), false},
		{"duplicate head", body + "\nReviewed head: " + head, false},
		{"contradictory head", body + "\nReviewed head: " + base, false},
		{"duplicate base", body + "\nBase: " + base, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			v, err := ParseVerdict([]byte("APPROVE\n" + fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateReviewBinding(v, head, base); (err == nil) != fixture.valid {
				t.Fatalf("binding: %v", err)
			}
		})
	}
}

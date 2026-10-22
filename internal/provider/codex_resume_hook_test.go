package provider

import "testing"

func TestCodexDeclaresDeferredExactResumeAttestation(t *testing.T) {
	var d Descriptor = mustCodex(t)
	attestor, ok := d.(ResumeIdentityAttestationDescriptor)
	if !ok || attestor.ExactResumeHookSource() != "resume" {
		t.Fatalf("codex resume attestation = %v ok=%v", d, ok)
	}
	hook := mustCodex(t).IdentityHook()
	if hook.Event != "SessionStart" || hook.SessionIDField != "session_id" ||
		hook.Timing != IdentityHookAfterFirstSubmission || !hook.RequiresNativeTrust {
		t.Fatalf("codex resume hook = %+v", hook)
	}
	captures, ok := d.(IdentityCaptureDescriptor)
	if !ok || len(captures.IdentityCaptureMechanisms()) != 1 ||
		captures.IdentityCaptureMechanisms()[0] != IdentityCaptureStructuredNativeHook {
		t.Fatal("codex resume must use its structured native hook")
	}
}

package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const LaunchTicketSchemaV1 = "noisedeck.supervisor-launch-ticket.v1"

var (
	ErrInvalidLaunchTicket  = errors.New("invalid supervisor launch ticket")
	ErrLaunchTicketDigest   = errors.New("supervisor launch ticket digest mismatch")
	ErrLaunchTicketEncoding = errors.New("supervisor launch ticket is not canonical")
	ErrLaunchTicketBinding  = errors.New("supervisor launch ticket does not match protected binding")
)

// LaunchTicketV1 is the exact content-free authority a supervisor releases to
// one authenticated launcher. It binds identities already established by the
// run, request, PTY, plan, transient unit, and launcher invocation records.
type LaunchTicketV1 struct {
	Schema               string `json:"schema"`
	RunID                string `json:"run_id"`
	SandboxGeneration    string `json:"sandbox_generation"`
	LaunchRequestDigest  string `json:"launch_request_digest"`
	PTYBindingDigest     string `json:"pty_binding_digest"`
	SandboxPlanDigest    string `json:"sandbox_plan_digest"`
	UnitName             string `json:"unit_name"`
	UnitPropertyDigest   string `json:"unit_property_digest"`
	LauncherInvocationID string `json:"launcher_invocation_id"`
}

// LaunchTicketRevision is the canonical ticket identity retained by the
// supervisor and checked by the launcher.
type LaunchTicketRevision struct {
	Digest        string
	CanonicalJSON string
	Ticket        LaunchTicketV1
}

// LaunchTicketBinding contains the protected identities a ticket must match.
// Copies inside a ticket confer no authority.
type LaunchTicketBinding struct {
	RunID                string
	SandboxGeneration    string
	LaunchRequestDigest  string
	PTYBindingDigest     string
	SandboxPlanDigest    string
	UnitName             string
	UnitPropertyDigest   string
	LauncherInvocationID string
}

// CanonicalizeLaunchTicket validates and digests one closed launch ticket.
func CanonicalizeLaunchTicket(ticket LaunchTicketV1) (*LaunchTicketRevision, error) {
	if !validLaunchTicket(ticket) {
		return nil, ErrInvalidLaunchTicket
	}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return nil, ErrInvalidLaunchTicket
	}
	digest := sha256.Sum256(raw)
	return &LaunchTicketRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Ticket:        ticket,
	}, nil
}

// VerifyLaunchTicket rejects noncanonical bytes and compares every identity
// with the supervisor's protected binding before the ticket can authorize a
// launcher.
func VerifyLaunchTicket(
	digest string,
	canonicalJSON string,
	binding LaunchTicketBinding,
) (*LaunchTicketRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, ErrLaunchTicketDigest
	}
	if !validLaunchTicketBinding(binding) {
		return nil, ErrLaunchTicketBinding
	}

	var ticket LaunchTicketV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ticket); err != nil {
		return nil, ErrLaunchTicketEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrLaunchTicketEncoding
	}
	revision, err := CanonicalizeLaunchTicket(ticket)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrLaunchTicketEncoding
	}
	if revision.Digest != digest {
		return nil, ErrLaunchTicketDigest
	}
	if !launchTicketMatchesBinding(ticket, binding) {
		return nil, ErrLaunchTicketBinding
	}
	return revision, nil
}

func validLaunchTicket(ticket LaunchTicketV1) bool {
	if ticket.Schema != LaunchTicketSchemaV1 ||
		!isCanonicalUUIDv4(ticket.RunID) ||
		!isCanonicalUUIDv4(ticket.SandboxGeneration) ||
		!isLowerSHA256(ticket.LaunchRequestDigest) ||
		!isLowerSHA256(ticket.PTYBindingDigest) ||
		!isLowerSHA256(ticket.SandboxPlanDigest) ||
		!isLowerSHA256(ticket.UnitPropertyDigest) ||
		!isCanonicalInvocationID(ticket.LauncherInvocationID) {
		return false
	}
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             ticket.RunID,
		SandboxGeneration: ticket.SandboxGeneration,
	})
	return err == nil && ticket.UnitName == unit.UnitName && ticket.UnitPropertyDigest == unit.Digest
}

func validLaunchTicketBinding(binding LaunchTicketBinding) bool {
	return validLaunchTicket(LaunchTicketV1{
		Schema:               LaunchTicketSchemaV1,
		RunID:                binding.RunID,
		SandboxGeneration:    binding.SandboxGeneration,
		LaunchRequestDigest:  binding.LaunchRequestDigest,
		PTYBindingDigest:     binding.PTYBindingDigest,
		SandboxPlanDigest:    binding.SandboxPlanDigest,
		UnitName:             binding.UnitName,
		UnitPropertyDigest:   binding.UnitPropertyDigest,
		LauncherInvocationID: binding.LauncherInvocationID,
	})
}

func launchTicketMatchesBinding(ticket LaunchTicketV1, binding LaunchTicketBinding) bool {
	return ticket.RunID == binding.RunID &&
		ticket.SandboxGeneration == binding.SandboxGeneration &&
		ticket.LaunchRequestDigest == binding.LaunchRequestDigest &&
		ticket.PTYBindingDigest == binding.PTYBindingDigest &&
		ticket.SandboxPlanDigest == binding.SandboxPlanDigest &&
		ticket.UnitName == binding.UnitName &&
		ticket.UnitPropertyDigest == binding.UnitPropertyDigest &&
		ticket.LauncherInvocationID == binding.LauncherInvocationID
}

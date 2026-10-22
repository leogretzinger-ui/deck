// Package experiment exposes the default-off, loopback-only, explicitly
// unconfined capacity-routing spike.
package experiment

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/capacity"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/session"
)

const probeTimeout = 45 * time.Second

const (
	CapabilityEnv    = "NOISEDECK_EXPERIMENT_CAPABILITY"
	CapabilityHeader = "X-Noisedeck-Experiment-Capability"
)

var opaqueValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Handler struct {
	enabled     bool
	capability  string
	collector   *capacity.Collector
	descriptors map[provider.Name]provider.Descriptor
	manager     *session.Manager
	projects    *project.Repository
	// trustHomes names the provider homes the harness-readiness diagnostic
	// reads. It is captured once, from the same environment a launch
	// inherits; tests point it at fixtures.
	trustHomes provider.TrustHomes
	log        zerolog.Logger
}

func New(enabled bool, capability string, collector *capacity.Collector,
	descriptors map[provider.Name]provider.Descriptor, manager *session.Manager,
	projects *project.Repository, log zerolog.Logger) *Handler {
	return &Handler{enabled: enabled, capability: capability, collector: collector, descriptors: descriptors,
		manager: manager, projects: projects, trustHomes: provider.TrustHomesFromEnvironment(), log: log}
}

// NewControlCapability creates the in-memory capability shared only with
// sessions launched by this explicitly unconfined Deck process.
func NewControlCapability() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func (h *Handler) Mount(handle func(string, http.Handler)) {
	handle("GET /api/experimental/harnesses", http.HandlerFunc(h.ListHarnesses))
	handle("GET /api/experimental/harnesses/{provider}", http.HandlerFunc(h.ShowHarness))
	handle("GET /api/experimental/projects/{id}/harness-readiness", http.HandlerFunc(h.HarnessReadiness))
	handle("POST /api/experimental/projects/{id}/agent-handoffs", http.HandlerFunc(h.AgentHandoff))
}

type harnessProfile struct {
	ID         provider.Name     `json:"id"`
	Configured bool              `json:"configured"`
	Evidence   capacity.Evidence `json:"evidence"`
}

type harnessList struct {
	Unconfined bool             `json:"unconfined"`
	Profiles   []harnessProfile `json:"profiles"`
}

// harnessReadiness reports one provisioned precondition per harness for one
// project: whether that harness has a recorded workspace-trust decision
// covering the project's directory.
//
// It reports; it does not gate. A provider's configuration cannot establish
// whether a launch will stop at a prompt, so this and the hook check beside it
// remain diagnostics and never gate a launch. A router that will not attend a
// modal can ask first and make its own decision.
type harnessReadiness struct {
	ID provider.Name `json:"id"`
	// WorkspaceTrust is "prepared", "absent" or "unknown", straight from the
	// provider's own record (docs/provider-contracts).
	WorkspaceTrust string `json:"workspace_trust"`
	// Record names the provider-owned file the answer came from, so you
	// have something to act on. Empty when nothing was measured.
	Record string `json:"workspace_trust_record,omitempty"`
	// Reason names why the state is unknown, and is empty otherwise.
	Reason string `json:"workspace_trust_unknown_reason,omitempty"`
}

type harnessReadinessList struct {
	Unconfined bool               `json:"unconfined"`
	Project    string             `json:"project"`
	Directory  string             `json:"directory"`
	Harnesses  []harnessReadiness `json:"harnesses"`
}

func (h *Handler) HarnessReadiness(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	p, err := h.projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			h.problem(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		h.problem(w, http.StatusInternalServerError, "internal", "project could not be read")
		return
	}
	names := make([]provider.Name, 0, len(h.descriptors))
	for name := range h.descriptors {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	out := make([]harnessReadiness, 0, len(names))
	for _, name := range names {
		trust := h.trustHomes.WorkspaceTrust(name, p.CanonicalPath)
		out = append(out, harnessReadiness{
			ID:             name,
			WorkspaceTrust: workspaceTrustLabel(trust.State),
			Record:         trust.RecordPath,
			Reason:         trust.Reason,
		})
	}
	h.json(w, http.StatusOK, harnessReadinessList{
		Unconfined: true, Project: p.ID, Directory: p.CanonicalPath, Harnesses: out,
	})
}

func workspaceTrustLabel(state provider.WorkspaceTrustState) string {
	switch state {
	case provider.WorkspaceTrustPrepared:
		return "prepared"
	case provider.WorkspaceTrustAbsent:
		return "absent"
	default:
		return "unknown"
	}
}

func (h *Handler) ListHarnesses(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	profiles := make([]harnessProfile, 0, len(h.descriptors))
	for _, name := range h.collector.Providers() {
		profiles = append(profiles, harnessProfile{
			ID: name, Configured: true, Evidence: h.collector.Collect(ctx, name),
		})
	}
	h.json(w, http.StatusOK, harnessList{Unconfined: true, Profiles: profiles})
}

func (h *Handler) ShowHarness(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	name := provider.Name(r.PathValue("provider"))
	if _, ok := h.descriptors[name]; !ok {
		h.problem(w, http.StatusNotFound, "provider_not_configured", "provider is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	h.json(w, http.StatusOK, struct {
		Unconfined bool              `json:"unconfined"`
		Evidence   capacity.Evidence `json:"evidence"`
	}{true, h.collector.Collect(ctx, name)})
}

type handoffRequest struct {
	Provider      provider.Name `json:"provider"`
	Model         string        `json:"model"`
	Role          string        `json:"role"`
	TaskID        string        `json:"task_id"`
	CorrelationID string        `json:"correlation_id"`
}

type HandoffResponse struct {
	Unconfined             bool              `json:"unconfined"`
	Session                *session.Session  `json:"session"`
	Evidence               capacity.Evidence `json:"evidence"`
	Stream                 string            `json:"stream"`
	Room                   string            `json:"room"`
	FromAgent              string            `json:"from_agent"`
	ToAgent                string            `json:"to_agent"`
	RequestedCorrelationID string            `json:"requested_correlation_id"`
	CorrelationID          string            `json:"correlation_id"`
	AcceptedType           string            `json:"accepted_type"`
	CompletedType          string            `json:"completed_type"`
}

func (h *Handler) AgentHandoff(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	var request handoffRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		h.problem(w, http.StatusBadRequest, "bad_request", "unreadable handoff request")
		return
	}
	if !opaqueValue.MatchString(request.Role) || !opaqueValue.MatchString(request.TaskID) ||
		!opaqueValue.MatchString(request.CorrelationID) {
		h.problem(w, http.StatusBadRequest, "bad_request", "role, task id, or correlation id is invalid")
		return
	}
	descriptor, ok := h.descriptors[request.Provider]
	if !ok {
		h.problem(w, http.StatusBadRequest, "provider_not_configured", "target provider is not configured")
		return
	}
	if _, ok := descriptor.(provider.DelegationDescriptor); !ok {
		h.problem(w, http.StatusConflict, "provider_not_supported", "target provider has no measured initial-prompt contract")
		return
	}
	p, err := h.projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			h.problem(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		h.problem(w, http.StatusInternalServerError, "internal", "project could not be read")
		return
	}
	if p.GitAuthorName == "" || p.GitAuthorEmail == "" || p.GitIdentityProfileID == nil ||
		p.GitIdentityProfileRevision == nil || p.GitIdentitySetupRevision < 1 {
		h.problem(w, http.StatusConflict, "git_identity_required", project.ErrGitIdentityRequired.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	evidence := h.collector.Collect(ctx, request.Provider)
	if evidence.State == "unknown" || len(evidence.Windows) == 0 {
		h.problem(w, http.StatusConflict, "capacity_unknown", "target subscription capacity is unknown")
		return
	}
	if !reportedModel(evidence.Models, request.Model) {
		h.problem(w, http.StatusConflict, "model_unknown", "target model was not reported by the provider")
		return
	}

	stream := "project/" + projectSlug(p.CanonicalPath)
	fromAgent := "delegate-" + roleSlug(request.Role) + "-" + string(request.Provider)
	toAgent := "source"
	room := "implementation"
	acceptedType := "handoff.accepted"
	completedType := "handoff.completed"
	correlationID, err := freshCorrelationID()
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "internal", "acceptance identity could not be created")
		return
	}
	prompt := bootstrapPrompt(request, stream, room, fromAgent, toAgent, correlationID, acceptedType, completedType)
	displayName := request.Role + " · " + request.Model
	s, err := h.manager.CreateDelegated(ctx, session.CreateSpec{
		ProjectID: p.ID, Provider: request.Provider, WorkDir: p.CanonicalPath,
		DisplayName: displayName,
	}, provider.DelegatedLaunch{Model: request.Model, Prompt: prompt})
	if err != nil {
		h.log.Warn().Str("project", p.ID).Str("provider", string(request.Provider)).Msg("unconfined handoff launch failed")
		h.problem(w, http.StatusConflict, "handoff_launch_failed", "delegated session could not be launched")
		return
	}
	s, err = h.manager.Present(ctx, s)
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "internal", "delegated session could not be observed")
		return
	}
	h.json(w, http.StatusCreated, HandoffResponse{
		Unconfined: true, Session: s, Evidence: evidence, Stream: stream, Room: room,
		FromAgent: fromAgent, ToAgent: toAgent, RequestedCorrelationID: request.CorrelationID,
		CorrelationID: correlationID,
		AcceptedType:  acceptedType,
		CompletedType: completedType,
	})
}

func bootstrapPrompt(request handoffRequest, stream, room, fromAgent, toAgent, correlationID, acceptedType, completedType string) string {
	return fmt.Sprintf("You are the delegated %s for Deck task %s in this same project. This is an explicitly unconfined capacity-routing experiment, not a custody transfer. Read AGENTS.md and run `noise tasks show %s` before acting. If you accept the assignment, run exactly: mempalace logstream append --type %s --stream %s --room %s --from-agent %s --to-agent %s --correlation-id %s --status claimed --json. Only that content-free event makes the handoff succeed. After it succeeds, continue the task in the delegated %s role. When that work is finished and verified, close the handoff exactly once with the same route and correlation: mempalace logstream append --type %s --stream %s --room %s --from-agent %s --to-agent %s --correlation-id %s --status applied --json, or --status failed when you cannot complete it. Only a content-free closure event ends the wait. Never attach a body, metadata, or artifacts to either event.",
		request.Role, request.TaskID, request.TaskID, acceptedType, stream, room,
		fromAgent, toAgent, correlationID, request.Role, completedType, stream, room,
		fromAgent, toAgent, correlationID)
}

func freshCorrelationID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "handoff-" + hex.EncodeToString(nonce[:]), nil
}

func reportedModel(models []capacity.Model, requested string) bool {
	for _, model := range models {
		if model.ID == requested {
			return true
		}
	}
	return false
}

func roleSlug(role string) string {
	return strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(role)), "-")
}

func projectSlug(path string) string {
	base := strings.ToLower(filepath.Base(path))
	var slug strings.Builder
	lastWasSeparator := false
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			slug.WriteRune(r)
			lastWasSeparator = false
		case slug.Len() > 0 && !lastWasSeparator:
			slug.WriteByte('-')
			lastWasSeparator = true
		}
	}
	result := strings.Trim(slug.String(), "-")
	if result == "" {
		return "project"
	}
	return result
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request) bool {
	if !h.enabled {
		h.problem(w, http.StatusNotFound, "experiment_disabled", "unconfined capacity routing is disabled")
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		h.problem(w, http.StatusForbidden, "loopback_required", "unconfined capacity routing accepts loopback requests only")
		return false
	}
	provided := r.Header.Get(CapabilityHeader)
	if h.capability == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(h.capability)) != 1 {
		h.problem(w, http.StatusForbidden, "capability_required", "unconfined capacity routing requires its process-local capability")
		return false
	}
	return true
}

func (h *Handler) json(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Noisedeck-Profile", "unconfined-capacity-routing-experiment")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) problem(w http.ResponseWriter, status int, code, message string) {
	h.json(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "unconfined": true})
}

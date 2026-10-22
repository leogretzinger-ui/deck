package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/reconciler"
	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/terminal"
)

// Diagnostics serves authenticated recorded-versus-observed state. It owns no
// lifecycle state; SQLite, tmux/process observation, the reconciler, the lease
// manager, and the live terminal proxy remain the respective authorities.
type Diagnostics struct {
	sessions   *session.Repository
	manager    *session.Manager
	reconciler *reconciler.Reconciler
	leases     *lease.Manager
	terminal   *terminal.Proxy
	log        zerolog.Logger
	now        func() time.Time
}

// NewDiagnostics wires the existing state owners into the HTTP projection.
func NewDiagnostics(sessions *session.Repository, manager *session.Manager,
	rec *reconciler.Reconciler, leases *lease.Manager, proxy *terminal.Proxy,
	log zerolog.Logger) *Diagnostics {
	return &Diagnostics{
		sessions: sessions, manager: manager, reconciler: rec,
		leases: leases, terminal: proxy, log: log, now: time.Now,
	}
}

// Mount registers the diagnostics behind the same authentication middleware
// as every other control API.
func (d *Diagnostics) Mount(handle func(string, http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/sessions/{id}/status", wrap(http.HandlerFunc(d.sessionStatus)))
	handle("GET /api/metrics", wrap(http.HandlerFunc(d.metrics)))
}

type recordedState struct {
	ID                    string                     `json:"id"`
	Status                session.Status             `json:"status"`
	Generation            int                        `json:"generation"`
	NativeSessionID       *string                    `json:"native_session_id"`
	ExitCode              *int                       `json:"exit_code"`
	ExitSignal            *string                    `json:"exit_signal"`
	ExitedAt              *time.Time                 `json:"exited_at"`
	ResumeAttempts        int                        `json:"resume_attempts"`
	IdentityCaptureLostAt *time.Time                 `json:"identity_capture_lost_at"`
	LastOutputAt          *time.Time                 `json:"last_output_at"`
	CreatedAt             time.Time                  `json:"created_at"`
	UpdatedAt             time.Time                  `json:"updated_at"`
	PresentationState     session.PresentationState  `json:"presentation_state"`
	RecoveryCapability    session.RecoveryCapability `json:"recovery_capability"`
	RecoveryState         session.RecoveryState      `json:"recovery_state"`
}

type observedState struct {
	PaneState         session.PaneObservation    `json:"pane_state"`
	ProcessState      session.ProcessObservation `json:"process_state"`
	PaneExists        *bool                      `json:"pane_exists"`
	PaneAlive         *bool                      `json:"pane_alive"`
	PanePID           *int                       `json:"pane_pid"`
	ProcessGroupID    *int                       `json:"process_group_id"`
	ProcessStartTicks *int64                     `json:"process_start_ticks"`
	IdentityVerified  *bool                      `json:"identity_verified"`
}

type reconciliationState struct {
	LastRun          time.Time `json:"last_run"`
	DiscrepancyCount int64     `json:"discrepancy_count"`
	HasDiscrepancy   *bool     `json:"has_discrepancy"`
}

type connectionState struct {
	AttachmentActive bool       `json:"attachment_active"`
	AttachmentCount  int        `json:"attachment_count"`
	ControllerActive bool       `json:"controller_active"`
	LeaseState       string     `json:"lease_state"`
	LeaseExpiry      *time.Time `json:"lease_expiry"`
	LastWriteResult  string     `json:"last_write_result"`
}

type sessionStatusResponse struct {
	Recorded       recordedState       `json:"recorded"`
	Observed       observedState       `json:"observed"`
	Reconciliation reconciliationState `json:"reconciliation"`
	Connection     connectionState     `json:"connection"`
}

func (d *Diagnostics) sessionStatus(w http.ResponseWriter, r *http.Request) {
	s, attempt, err := d.sessions.RecoveryPresentationSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			d.error(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		d.log.Error().Err(err).Msg("diagnostic session read failed")
		d.error(w, http.StatusInternalServerError, "internal", "could not read session status")
		return
	}
	observation := d.manager.ObserveRuntime(r.Context(), s)
	if observation.Issue != nil {
		d.log.Warn().Err(observation.Issue).Str("session", s.ID).
			Msg("session diagnostic observation incomplete")
	}

	presented := d.manager.PresentObserved(s, observation, attempt)
	response := sessionStatusResponse{
		Recorded: recordedState{
			ID: s.ID, Status: s.Status, Generation: s.Generation,
			NativeSessionID: s.NativeSessionID, ExitCode: s.ExitCode,
			ExitSignal: s.ExitSignal, ExitedAt: s.ExitedAt,
			ResumeAttempts:        s.ResumeAttempts,
			IdentityCaptureLostAt: s.IdentityCaptureLostAt,
			LastOutputAt:          s.LastOutputAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
			PresentationState:  presented.PresentationState,
			RecoveryCapability: presented.RecoveryCapability,
			RecoveryState:      presented.RecoveryState,
		},
		Observed: mapObserved(observation),
		Reconciliation: reconciliationState{
			LastRun:          d.reconciler.LastPass(),
			DiscrepancyCount: d.reconciler.Discrepancies(),
			HasDiscrepancy:   session.RuntimeDiscrepancy(s, observation),
		},
		Connection: d.connection(r, s.ID),
	}
	d.json(w, response)
}

func mapObserved(observation session.RuntimeObservation) observedState {
	response := observedState{
		PaneState: observation.Pane, ProcessState: observation.Process,
		IdentityVerified: observation.IdentityVerified,
	}
	switch observation.Pane {
	case session.PaneAlive:
		exists, alive := true, true
		response.PaneExists, response.PaneAlive = &exists, &alive
	case session.PaneDead:
		exists, alive := true, false
		response.PaneExists, response.PaneAlive = &exists, &alive
	case session.PaneAbsent:
		exists, alive := false, false
		response.PaneExists, response.PaneAlive = &exists, &alive
	}
	if info := observation.PaneInfo; info != nil {
		response.PanePID = &info.PID
		if !info.Dead {
			response.ProcessGroupID = &info.ProcessGroupID
			response.ProcessStartTicks = &info.StartTicks
		}
	}
	return response
}

func (d *Diagnostics) connection(r *http.Request, sessionID string) connectionState {
	snapshot := d.terminal.ConnectionSnapshot(sessionID)
	result := connectionState{
		AttachmentActive: snapshot.AttachmentCount > 0,
		AttachmentCount:  snapshot.AttachmentCount,
		LeaseState:       "none", LastWriteResult: snapshot.LastWriteResult,
	}
	if result.LastWriteResult == "" {
		result.LastWriteResult = "none"
	}

	l, err := d.leases.Get(r.Context(), sessionID)
	switch {
	case errors.Is(err, lease.ErrNotFound):
		return result
	case err != nil:
		result.LeaseState = "unavailable"
		d.log.Warn().Err(err).Str("session", sessionID).Msg("diagnostic lease read failed")
		return result
	}
	result.LeaseExpiry = &l.ExpiresAt
	if l.Expired(d.now()) {
		result.LeaseState = "expired"
		return result
	}
	result.LeaseState = "held"
	result.ControllerActive = snapshot.AttachmentCount == 1 &&
		snapshot.FencingToken == l.FencingToken
	return result
}

type metricsResponse struct {
	ReconcilerLastRun         time.Time `json:"reconciler_last_run"`
	ReconcilerInterval        string    `json:"reconciler_interval"`
	DiscrepancyCount          int64     `json:"discrepancy_count"`
	ActiveSessions            int       `json:"active_sessions"`
	AutomaticRecoveryPending  int       `json:"automatic_recovery_pending"`
	AutomaticRecoveryInFlight int       `json:"automatic_recovery_in_flight"`
}

func (d *Diagnostics) metrics(w http.ResponseWriter, r *http.Request) {
	counts, err := d.sessions.Counts(r.Context())
	if err != nil {
		d.log.Error().Err(err).Msg("diagnostic fleet count failed")
		d.error(w, http.StatusInternalServerError, "internal", "could not read fleet metrics")
		return
	}
	recovery, err := d.manager.RecoveryCounts(r.Context())
	if err != nil {
		d.log.Error().Err(err).Msg("diagnostic recovery count failed")
		d.error(w, http.StatusInternalServerError, "internal", "could not read fleet metrics")
		return
	}
	d.json(w, metricsResponse{
		ReconcilerLastRun:         d.reconciler.LastPass(),
		ReconcilerInterval:        d.reconciler.Interval().String(),
		DiscrepancyCount:          d.reconciler.Discrepancies(),
		ActiveSessions:            counts.Active,
		AutomaticRecoveryPending:  recovery.AutomaticPending,
		AutomaticRecoveryInFlight: recovery.AutomaticInFlight,
	})
}

func (d *Diagnostics) json(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func (d *Diagnostics) error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/hookcapture"
)

// SuccessorSlot is the only durable recovery ceremony. It allocates one
// generation and marker before spawn; the deterministic tmux target supplies
// crash rediscovery, and live observation supplies the complete process tuple.
type SuccessorSlot struct {
	SessionID                string
	SourceGeneration         int
	SourceControllerRevision int64
	CandidateGeneration      int
	CandidateIdentityMarker  string
	CaptureTokenHash         *string
	CaptureState             *hookcapture.GrantState
	// CandidatePanePID and its companions are the spawn's process-scoped
	// identity, written once the spawn is observed. They are what separates
	// this candidate from any later occupant of the same pane: marker,
	// generation, and launch marker all survive an out-of-band respawn, and
	// only these move. A slot still carrying nil has no proven occupant.
	CandidatePanePID        *int
	CandidateProcessGroupID *int
	CandidateStartTicks     *int64
	CreatedAt               time.Time
}

// RecordCandidateProcess pins the observed spawn to the slot. It is written
// after the pane is observed and before any path may adopt that pane as the
// row's controller, so a crash between spawn and this write leaves a slot that
// proves nothing rather than one that proves the wrong thing.
//
// The pid alone is required. A candidate that was already dead when first
// observed has no readable process group or start ticks — those are live-only
// — but tmux retains the pid that exited, and that retained pid is exactly
// what tells this candidate's corpse from a pane somebody else respawned.
// Recording it lets a dead candidate still be recognized, converged, and
// retired; refusing to record one would strand its slot forever and, with it,
// every later attempt to recover the session.
func (r *Repository) RecordCandidateProcess(ctx context.Context, slot *SuccessorSlot,
	pid, pgid int, startTicks int64) error {
	if slot == nil || pid <= 1 {
		return ErrInvalidRecoveryCandidate
	}
	var pgidValue, ticksValue any
	if pgid > 1 && startTicks > 0 {
		pgidValue, ticksValue = pgid, startTicks
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_successor_slots
			SET candidate_pane_pid = ?, candidate_process_group_id = ?,
				candidate_start_ticks = ?
			WHERE session_id = ? AND candidate_generation = ?
			  AND candidate_identity_marker = ?`,
			pid, pgidValue, ticksValue, slot.SessionID, slot.CandidateGeneration,
			slot.CandidateIdentityMarker)
		if err != nil {
			return fmt.Errorf("session %s: record candidate process: %w", slot.SessionID, err)
		}
		if err := mustAffectOne(res, slot.SessionID); err != nil {
			return ErrRecoverySuperseded
		}
		slot.CandidatePanePID = &pid
		slot.CandidateProcessGroupID, slot.CandidateStartTicks = nil, nil
		if pgidValue != nil {
			slot.CandidateProcessGroupID, slot.CandidateStartTicks = &pgid, &startTicks
		}
		return nil
	})
}

// BeginSuccessorSlot advances the diagnostic row to an unattached generation
// and consumes its one automatic opportunity in the same transaction that
// creates the slot. An optional exact-resume grant is inserted atomically.
// assignedNativeID is non-empty only for a fresh-relaunch candidate of an
// assigned-identity provider: the id Deck will embed in the launch argv IS the
// new conversation's identity, and it is recorded with the allocation so the
// committed successor is resumable the moment it runs — commit itself never
// writes native identity. Assigned ids are single-use (a refused first launch
// still burns its uuid), so the caller mints a fresh one per candidate and
// never replays a forgotten or refused id. A fresh candidate takes a
// launch-shaped (startup) grant or none; only an exact resume takes a resume
// grant.
func (r *Repository) BeginSuccessorSlot(ctx context.Context, source *Session,
	marker string, grant *hookcapture.Grant, assignedNativeID string) (*SuccessorSlot, error) {
	if source == nil || source.ID == "" || marker == "" || source.Generation < 1 ||
		source.ControllerRevision < 0 || !terminalRecoverySource(source) ||
		source.RemovedAt != nil || source.ResumeAttempts != 0 {
		return nil, ErrInvalidRecoveryCandidate
	}
	if assignedNativeID != "" && (source.NativeSessionID != nil || (grant != nil &&
		grant.ExpectedSource != hookcapture.ExpectedSourceStartup)) {
		return nil, ErrInvalidRecoveryCandidate
	}
	candidateGeneration := source.Generation + 1
	var captureHash *string
	if grant != nil {
		// A resume candidate's grant is whichever shape the provider's
		// callback actually emits: claude attests a resume-shaped source,
		// while omp's session_start redelivers the resumed id with the
		// startup source a fresh launch posts. The flight prepared the
		// grant a few lines above, so both shapes are admitted here and the
		// exact assignment rule below stays strict: a fresh assigned
		// candidate takes a startup grant and no prior identity.
		freshSource := assignedNativeID != ""
		sourceOK := grant.ExpectedSource == hookcapture.ExpectedSourceStartup ||
			(!freshSource && grant.ExpectedSource == hookcapture.ExpectedSourceResume)
		if !validIdentityCaptureGrant(*grant) || !sourceOK ||
			grant.SessionID != source.ID || grant.Provider != source.Provider ||
			grant.Generation != candidateGeneration || grant.IdentityMarker != marker {
			return nil, ErrInvalidRecoveryCandidate
		}
		captureHash = &grant.TokenHash
	}
	slot := &SuccessorSlot{
		SessionID: source.ID, SourceGeneration: source.Generation,
		SourceControllerRevision: source.ControllerRevision,
		CandidateGeneration:      candidateGeneration, CandidateIdentityMarker: marker,
		CaptureTokenHash: captureHash,
	}
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `
			SELECT 1 FROM session_successor_slots WHERE session_id = ?`, source.ID).Scan(&one)
		if err == nil {
			return ErrRecoveryInProgress
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %s: check successor slot: %w", source.ID, err)
		}
		err = tx.QueryRowContext(ctx, `
			SELECT 1 FROM session_stop_attempts
			WHERE session_id = ? AND state IN (?, ?)`, source.ID,
			stopClaimed, stopExecuting).Scan(&one)
		if err == nil {
			return ErrControllerActionInProgress
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %s: check stop fence: %w", source.ID, err)
		}

		now := time.Now()
		if grant != nil {
			if err := insertIdentityCaptureTx(ctx, tx, *grant, now); err != nil {
				return err
			}
		}
		// The series count advances in the same statement that allocates the
		// generation, so a relaunch cannot happen without being counted. A
		// series older than the window is not a series: it restarts at one.
		windowStart := db.FormatTime(now.Add(-automaticRecoveryWindow))
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions SET generation = ?, identity_marker = ?,
				native_session_id = CASE WHEN ? <> ''
					THEN ? ELSE native_session_id END,
				resume_attempts = 1, resume_next_at = NULL,
				resume_series_started_at = NULL,
				resume_probation_until = NULL,
				automatic_recoveries = CASE
					WHEN automatic_recovery_series_at IS NULL
					  OR automatic_recovery_series_at < ? THEN 1
					ELSE automatic_recoveries + 1 END,
				automatic_recovery_series_at = CASE
					WHEN automatic_recovery_series_at IS NULL
					  OR automatic_recovery_series_at < ? THEN ?
					ELSE automatic_recovery_series_at END,
				updated_at = ?
			WHERE id = ? AND removed_at IS NULL AND status IN (?, ?)
			  AND generation = ? AND identity_marker IS ?
			  AND controller_revision = ? AND resume_attempts = 0
			  AND stop_requested_at IS NULL
			  AND (automatic_recovery_series_at IS NULL
			       OR automatic_recovery_series_at < ?
			       OR automatic_recoveries < ?)`,
			candidateGeneration, marker, assignedNativeID, assignedNativeID,
			windowStart, windowStart,
			db.FormatTime(now), db.FormatTime(now), source.ID,
			StatusExited, StatusInterrupted, source.Generation,
			source.IdentityMarker, source.ControllerRevision,
			windowStart, maxAutomaticRecoveries)
		if err != nil {
			return fmt.Errorf("session %s: allocate successor generation: %w", source.ID, err)
		}
		if err := mustAffectOne(res, source.ID); err != nil {
			return ErrRecoverySuperseded
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO session_successor_slots (
				session_id, source_generation, source_controller_revision,
				candidate_generation, candidate_identity_marker,
				capture_token_sha256, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, source.ID, source.Generation,
			source.ControllerRevision, candidateGeneration, marker,
			captureHash, db.FormatTime(now))
		if err != nil {
			return fmt.Errorf("session %s: persist successor slot: %w", source.ID, err)
		}
		slot.CreatedAt = now
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &source.ID, Type: AuditResumeAttempt,
			Details: "automatic successor generation allocated before launch",
		})
	})
	if err != nil {
		return nil, err
	}
	return slot, nil
}

// CommitSuccessorRunning publishes a fully observed candidate: the complete
// pane and process tuple, matching the slot this recovery allocated.
//
// Provider attestation is not part of the CAS. Whether the provider confirmed
// the resume through its hook decides what is KNOWN about the successor, never
// whether a live pane may be recorded; a process the user can see and type
// into is running whether or not a third-party callback arrived to say so.
func (r *Repository) CommitSuccessorRunning(ctx context.Context, slot *SuccessorSlot,
	candidate *Session) (bool, error) {
	if slot == nil || candidate == nil || candidate.ID != slot.SessionID ||
		candidate.Generation != slot.CandidateGeneration || candidate.IdentityMarker == nil ||
		*candidate.IdentityMarker != slot.CandidateIdentityMarker ||
		candidate.TmuxSessionID == nil || candidate.TmuxPaneID == nil ||
		candidate.PanePID == nil || candidate.ProcessGroupID == nil ||
		candidate.StartTicks == nil || candidate.ExpectedExecutable == nil ||
		candidate.BootEpoch == nil {
		return false, ErrInvalidRecoveryCandidate
	}
	committed := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := db.FormatTime(time.Now())
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions SET tmux_socket_path = ?, tmux_session_name = ?,
				tmux_session_id = ?, tmux_pane_id = ?, pane_pid = ?,
				process_group_id = ?, process_start_ticks = ?,
				expected_executable = ?, boot_epoch = ?, status = ?,
				exit_code = NULL, exit_signal = NULL, exited_at = NULL,
				stop_requested_at = NULL, resume_attempts = 0,
				resume_next_at = NULL, resume_series_started_at = NULL,
				resume_probation_until = NULL, updated_at = ?,
				generation_started_at = ?, generation_resumed = ?
			WHERE id = ? AND removed_at IS NULL AND status IN (?, ?)
			  AND generation = ? AND identity_marker = ?
			  AND controller_revision = ?
			  AND EXISTS (
			    SELECT 1 FROM session_successor_slots slot
			    WHERE slot.session_id = sessions.id
			      AND slot.source_generation = ?
			      AND slot.source_controller_revision = ?
			      AND slot.candidate_generation = sessions.generation
			      AND slot.candidate_identity_marker = sessions.identity_marker
			  )`,
			candidate.TmuxSocketPath, candidate.TmuxSessionName,
			candidate.TmuxSessionID, candidate.TmuxPaneID, candidate.PanePID,
			candidate.ProcessGroupID, candidate.StartTicks,
			candidate.ExpectedExecutable, candidate.BootEpoch, StatusRunning,
			now, now, candidate.GenerationResumed,
			slot.SessionID, StatusExited, StatusInterrupted,
			slot.CandidateGeneration, slot.CandidateIdentityMarker,
			slot.SourceControllerRevision+1, slot.SourceGeneration,
			slot.SourceControllerRevision)
		if err != nil {
			return fmt.Errorf("session %s: commit successor: %w", slot.SessionID, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		res, err = tx.ExecContext(ctx, `
			DELETE FROM session_successor_slots
			WHERE session_id = ? AND source_generation = ?
			  AND source_controller_revision = ? AND candidate_generation = ?
			  AND candidate_identity_marker = ?`, slot.SessionID,
			slot.SourceGeneration, slot.SourceControllerRevision,
			slot.CandidateGeneration, slot.CandidateIdentityMarker)
		if err != nil {
			return err
		}
		if err := mustAffectOne(res, slot.SessionID); err != nil {
			return err
		}
		committed = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &slot.SessionID, Type: AuditResumed,
			Details: "verified successor committed as the current controller",
		})
	})
	return committed, err
}

// RetireSuccessorSlot closes a candidate only after the caller proved it
// absent or completed its exact-tuple cleanup. Uncertainty leaves the slot.
func (r *Repository) RetireSuccessorSlot(ctx context.Context, slot *SuccessorSlot,
	diagnostic string) (bool, error) {
	if slot == nil {
		return false, ErrInvalidRecoveryCandidate
	}
	retired := false
	detail := "provisional successor retired without controller commit"
	if diagnostic != "" {
		detail += ": " + diagnostic
	}
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM session_successor_slots
			WHERE session_id = ? AND source_generation = ?
			  AND source_controller_revision = ? AND candidate_generation = ?
			  AND candidate_identity_marker = ?`, slot.SessionID,
			slot.SourceGeneration, slot.SourceControllerRevision,
			slot.CandidateGeneration, slot.CandidateIdentityMarker)
		if err != nil {
			return fmt.Errorf("session %s: retire successor slot: %w", slot.SessionID, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		retired = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &slot.SessionID, Type: AuditResumeAttempt,
			Details: detail,
		})
	})
	return retired, err
}

// ReturnAbortedRecoveryOpportunity retires the slot and hands back the
// spent admission when DECK stopped a recovery before the provider answered
// — the sweep budget expiring mid-flight, or a shutdown. The slot removal
// and the reopened opportunity land in one transaction, and the automatic
// recovery series count is deliberately untouched: every re-admission
// counts another attempt, so a schedule that keeps aborting recoveries
// still runs into the two-in-ten-minutes bound instead of retrying forever.
//
// The identity the admission allocated is undone with the same stroke: a
// fresh candidate's assigned uuid is burned by an unlaunched flight, and a
// row left carrying it would RESUME an id the provider has never seen
// instead of relaunching fresh. priorNativeID is the identity the row
// carried before this admission — NULL for a fresh candidate, the recorded
// conversation id for a resume — and it is what the row goes back to.
//
// A provider's answer never passes through here. A refused resume, a gone
// store, or an executable that will not run tells the user something
// about the session, and its opportunity stays spent; only a flight that
// asked nothing and learned nothing gives the attempt back.
func (r *Repository) ReturnAbortedRecoveryOpportunity(ctx context.Context,
	slot *SuccessorSlot, priorNativeID *string, detail string) (bool, error) {
	if slot == nil {
		return false, ErrInvalidRecoveryCandidate
	}
	if detail == "" {
		detail = "automatic recovery aborted before the provider answered; opportunity returned"
	}
	var restoreNativeID any
	if priorNativeID != nil {
		restoreNativeID = *priorNativeID
	}
	returned := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM session_successor_slots
			WHERE session_id = ? AND source_generation = ?
			  AND source_controller_revision = ? AND candidate_generation = ?
			  AND candidate_identity_marker = ?`, slot.SessionID,
			slot.SourceGeneration, slot.SourceControllerRevision,
			slot.CandidateGeneration, slot.CandidateIdentityMarker)
		if err != nil {
			return fmt.Errorf("session %s: return aborted recovery: %w", slot.SessionID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// The slot is already gone: a commit, a refusal, or another
			// unwind owned it, and none of them may be reopened here.
			return nil
		}
		// The CAS matches exactly the row BeginSuccessorSlot wrote — the
		// candidate generation and marker — so a row that moved on (a
		// committed successor, a user action) is never reopened.
		res, err = tx.ExecContext(ctx, `
			UPDATE sessions SET resume_attempts = 0, resume_next_at = NULL,
				resume_series_started_at = NULL, native_session_id = ?,
				updated_at = ?
			WHERE id = ? AND removed_at IS NULL AND status IN (?, ?)
			  AND generation = ? AND identity_marker = ? AND resume_attempts = 1`,
			restoreNativeID, db.FormatTime(time.Now()), slot.SessionID,
			StatusExited, StatusInterrupted,
			slot.CandidateGeneration, slot.CandidateIdentityMarker)
		if err != nil {
			return fmt.Errorf("session %s: return aborted recovery opportunity: %w", slot.SessionID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		returned = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &slot.SessionID, Type: AuditResumeAttempt,
			Details: detail,
		})
	})
	return returned, err
}

func (r *Repository) SuccessorSlot(ctx context.Context, sessionID string) (*SuccessorSlot, error) {
	slot, err := scanSuccessorSlot(r.db.Reader().QueryRowContext(ctx,
		successorSlotSelect+` WHERE slot.session_id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return slot, err
}

func (r *Repository) ListSuccessorSlots(ctx context.Context) ([]*SuccessorSlot, error) {
	rows, err := r.db.Reader().QueryContext(ctx, successorSlotSelect+` ORDER BY slot.created_at`)
	if err != nil {
		return nil, fmt.Errorf("session: list successor slots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var slots []*SuccessorSlot
	for rows.Next() {
		slot, err := scanSuccessorSlot(rows)
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

const successorSlotSelect = `SELECT slot.session_id, slot.source_generation,
	slot.source_controller_revision, slot.candidate_generation,
	slot.candidate_identity_marker, slot.capture_token_sha256, grant.state,
	slot.candidate_pane_pid, slot.candidate_process_group_id,
	slot.candidate_start_ticks, slot.created_at
	FROM session_successor_slots slot
	LEFT JOIN provider_identity_capture_grants grant
	  ON grant.token_sha256 = slot.capture_token_sha256`

func scanSuccessorSlot(row scanner) (*SuccessorSlot, error) {
	var slot SuccessorSlot
	var captureState *string
	var created string
	if err := row.Scan(&slot.SessionID, &slot.SourceGeneration,
		&slot.SourceControllerRevision, &slot.CandidateGeneration,
		&slot.CandidateIdentityMarker, &slot.CaptureTokenHash,
		&captureState, &slot.CandidatePanePID, &slot.CandidateProcessGroupID,
		&slot.CandidateStartTicks, &created); err != nil {
		return nil, err
	}
	if captureState != nil {
		state := hookcapture.GrantState(*captureState)
		slot.CaptureState = &state
	}
	var err error
	slot.CreatedAt, err = time.Parse(db.TimeFormat, created)
	if err != nil {
		return nil, fmt.Errorf("session %s: successor created_at: %w", slot.SessionID, err)
	}
	return &slot, nil
}

func (r *Repository) RecoveryPresentationSnapshot(ctx context.Context,
	sessionID string) (*Session, *SuccessorSlot, error) {
	tx, err := r.db.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	s, err := scanFrom(tx.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, sessionID))
	if err != nil {
		return nil, nil, err
	}
	slot, err := scanSuccessorSlot(tx.QueryRowContext(ctx,
		successorSlotSelect+` WHERE slot.session_id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		slot = nil
	} else if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return s, slot, nil
}

func (r *Repository) RecoveryFactsSnapshot(ctx context.Context) ([]*Session, []*SuccessorSlot, error) {
	candidates, err := r.ListResumeCandidates(ctx)
	if err != nil {
		return nil, nil, err
	}
	slots, err := r.ListSuccessorSlots(ctx)
	return candidates, slots, err
}

// LegacyRecoveryAttempt is a one-release reader for migration 025 rows. New
// recovery never writes that table; boot either proves an old candidate absent
// or cleans only its exact rediscovered tuple before recording the receipt.
type LegacyRecoveryAttempt struct {
	SessionID, Nonce, State, TmuxSocketPath, TmuxSessionName string
	CandidateGeneration                                      int
	CandidateIdentityMarker, ExpectedExecutable, BootEpoch   string
}

func (r *Repository) ListLegacyRecoveryAttempts(ctx context.Context) ([]*LegacyRecoveryAttempt, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT session_id, nonce, state, candidate_generation,
			candidate_identity_marker, candidate_tmux_socket_path,
			candidate_tmux_session_name, candidate_expected_executable,
			candidate_boot_epoch
		FROM session_recovery_attempts
		WHERE state IN ('starting', 'executing') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*LegacyRecoveryAttempt
	for rows.Next() {
		var item LegacyRecoveryAttempt
		if err := rows.Scan(&item.SessionID, &item.Nonce, &item.State,
			&item.CandidateGeneration, &item.CandidateIdentityMarker,
			&item.TmuxSocketPath, &item.TmuxSessionName,
			&item.ExpectedExecutable, &item.BootEpoch); err != nil {
			return nil, err
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (r *Repository) FinishLegacyRecoveryAttempt(ctx context.Context,
	attempt *LegacyRecoveryAttempt, state string) error {
	if attempt == nil || (state != "failed" && state != "uncertain") {
		return ErrInvalidRecoveryCandidate
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_recovery_attempts
			SET state = ?, cleanup_token = NULL, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state IN ('starting', 'executing')`,
			state, db.FormatTime(time.Now()), attempt.SessionID, attempt.Nonce)
		if err != nil {
			return err
		}
		return mustAffectOne(res, attempt.SessionID)
	})
}

func (r *Repository) RecordLegacyRecoveryReceipt(ctx context.Context) error {
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_recovery_attempts
			WHERE state IN ('starting', 'executing')`).Scan(&open); err != nil {
			return err
		}
		if open != 0 {
			return ErrRecoveryInProgress
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO session_recovery_upgrade_receipt(singleton, completed_at)
			VALUES (1, ?) ON CONFLICT(singleton) DO NOTHING`, db.FormatTime(time.Now()))
		return err
	})
}

// ReopenRecoveryOpportunity re-arms a terminal row's single automatic attempt
// at a user's explicit request.
//
// The automatic opportunity is one-shot because an unattended loop that keeps
// relaunching a provider is a way to burn a user's money and rate limits
// while they are asleep. A person pressing "retry" is not that loop: they have
// read the failure, they own the consequence, and refusing them means a
// transient tmux or provider hiccup permanently buries work the CLI could
// still resume. The row must still be terminal and un-removed; this grants
// another attempt, never a second live generation. A recorded stop request
// clears with the counters: asking for the session back rescinds the stop,
// and a successor generation must not inherit a stop aimed at its
// predecessor.
func (r *Repository) ReopenRecoveryOpportunity(ctx context.Context, s *Session) (bool, error) {
	if s == nil || !terminalRecoverySource(s) {
		return false, ErrInvalidRecoveryCandidate
	}
	reopened := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		reopened = false
		var one int
		err := tx.QueryRowContext(ctx, `
			SELECT 1 FROM session_successor_slots WHERE session_id = ?`, s.ID).Scan(&one)
		if err == nil {
			return ErrRecoveryInProgress
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %s: check successor slot: %w", s.ID, err)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions SET resume_attempts = 0, resume_next_at = NULL,
				resume_series_started_at = NULL, resume_probation_until = NULL,
				automatic_recoveries = 0, automatic_recovery_series_at = NULL,
				stop_requested_at = NULL,
				updated_at = ?
			WHERE id = ? AND removed_at IS NULL AND status IN (?, ?)
			  AND generation = ? AND controller_revision = ?`,
			db.FormatTime(time.Now()), s.ID, StatusExited, StatusInterrupted,
			s.Generation, s.ControllerRevision)
		if err != nil {
			return fmt.Errorf("session %s: reopen recovery: %w", s.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		reopened = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &s.ID, Type: AuditResumeAttempt,
			Details: "user reopened the recovery opportunity",
		})
	})
	return reopened, err
}

// CloseRecoveryOpportunity spends a terminal row's automatic recovery
// opportunity without launching anything, and records why.
//
// It answers a resume the provider did not continue — the store the
// conversation lived in is gone by name, or the resumed process was gone
// before it could attest. Asking again unattended only repeats the same
// question, which is the relaunch loop the recovery bound also guards. The
// identity stays on the row: the provider's answer can change once the
// user has logged in or restored a volume, and a forgotten identity
// would make that conversation unreachable for good. The user's explicit
// resume re-arms the opportunity; a fresh conversation is their separate
// choice.
func (r *Repository) CloseRecoveryOpportunity(ctx context.Context, s *Session, detail string) error {
	if s == nil || s.ID == "" {
		return nil
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return closeRecoveryOpportunityTx(ctx, tx, s, detail)
	})
}

// closeRecoveryOpportunityTx spends the opportunity inside a caller's
// transaction, so an exit and the judgement it carries are recorded together
// and no observer can see one without the other.
func closeRecoveryOpportunityTx(ctx context.Context, tx *sql.Tx, s *Session, detail string) error {
	if s == nil || s.ID == "" {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE sessions SET resume_attempts = 1, resume_next_at = NULL,
			resume_series_started_at = NULL, updated_at = ?
		WHERE id = ? AND removed_at IS NULL AND generation = ?`,
		db.FormatTime(time.Now()), s.ID, s.Generation)
	if err != nil {
		return fmt.Errorf("session %s: close recovery opportunity: %w", s.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	s.ResumeAttempts = 1
	return db.AppendAudit(ctx, tx, db.AuditEvent{
		SessionID: &s.ID, Type: AuditResumeAttempt, Details: detail,
	})
}

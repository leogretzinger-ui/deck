package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
)

// AttentionEvent is the adapter's bounded, content-free projection. It cannot
// carry prompts, tool arguments, approval bodies, or model output.
type AttentionEvent struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Sequence  int64  `json:"sequence"`
	Turn      string `json:"turn"`
	Revision  string `json:"revision"`
	Kind      string `json:"kind"`
}

func attentionID(s string, size int) bool {
	if len(s) != size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

// AttentionHook uses the existing private hook channel's per-generation bearer.
// It neither captures nor changes native identity, and has no browser authority.
func (r *Repository) AttentionHook() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		token := req.Header.Get("X-Noisedeck-Hook-Token")
		if len(token) != 32 {
			http.Error(w, "hook credential unavailable", http.StatusGone)
			return
		}
		var event AttentionEvent
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096))
		dec.DisallowUnknownFields()
		var trailing any
		if err := dec.Decode(&event); err != nil {
			http.Error(w, "invalid attention event", 400)
			return
		}
		if dec.Decode(&trailing) != io.EOF {
			http.Error(w, "invalid attention event", 400)
			return
		}
		sum := sha256.Sum256([]byte(token))
		err := r.AcceptAttention(req.Context(), hex.EncodeToString(sum[:]), event)
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, hookcapture.ErrUnknownToken):
			http.Error(w, "hook credential unavailable", http.StatusGone)
		case errors.Is(err, hookcapture.ErrConflict):
			http.Error(w, "attention event conflicts with current conversation", http.StatusConflict)
		default:
			http.Error(w, "could not record attention", 500)
		}
	}
}

func (r *Repository) AcceptAttention(ctx context.Context, tokenHash string, ev AttentionEvent) error {
	if ev.Sequence < 1 || ev.Sequence > 9007199254740991 || !attentionID(ev.Turn, 64) || !attentionID(ev.Revision, 32) {
		return hookcapture.ErrConflict
	}
	state := ev.Kind
	switch state {
	case "started":
		state = "working"
	case "completed":
		state = "idle"
	case "working", "input", "approval", "unknown":
	default:
		return hookcapture.ErrConflict
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var id, provider, workdir, marker, grantState string
		var generation int
		err := tx.QueryRowContext(ctx, `SELECT session_id, provider, canonical_work_dir, generation, identity_marker, state
    FROM provider_identity_capture_grants WHERE token_sha256 = ?`, tokenHash).Scan(&id, &provider, &workdir, &generation, &marker, &grantState)
		if errors.Is(err, sql.ErrNoRows) {
			return hookcapture.ErrUnknownToken
		}
		if err != nil {
			return err
		}
		if (provider != "codex" && provider != "claude") || (grantState != capturePending && grantState != captureConsumed) {
			return hookcapture.ErrUnknownToken
		}
		if hookcapture.ValidateEvent(provider, workdir, hookcapture.Event{SessionID: ev.SessionID, CWD: ev.CWD}) != nil {
			return hookcapture.ErrConflict
		}
		var native *string
		var currentState, turn, completion, completionTurn, eventHash string
		var sequence int64
		err = tx.QueryRowContext(ctx, `SELECT native_session_id, attention_state, attention_turn, attention_sequence, completion_revision, completion_turn, attention_event_hash
    FROM sessions WHERE id = ? AND provider = ? AND generation = ? AND identity_marker = ?
    AND removed_at IS NULL AND status IN ('starting','running')`, id, provider, generation, marker).Scan(&native, &currentState, &turn, &sequence, &completion, &completionTurn, &eventHash)
		if errors.Is(err, sql.ErrNoRows) {
			return hookcapture.ErrConflict
		}
		if err != nil {
			return err
		}
		if native == nil || *native != ev.SessionID {
			return hookcapture.ErrConflict
		}
		// Retrying an accepted snapshot is harmless, including after acknowledgment.
		encoded, _ := json.Marshal(ev)
		digest := sha256.Sum256(encoded)
		hash := hex.EncodeToString(digest[:])
		if ev.Sequence == sequence {
			if eventHash != hash {
				return hookcapture.ErrConflict
			}
			return nil
		}
		if ev.Sequence < sequence {
			return hookcapture.ErrConflict
		}
		if ev.Kind != "started" && (turn != ev.Turn || turn == "") {
			return hookcapture.ErrConflict
		}
		// An old Stop must not resurrect a completion after it has been seen.
		if ev.Kind == "completed" {
			if currentState == "idle" {
				return hookcapture.ErrConflict
			}
			completion = ev.Revision
			completionTurn = ev.Turn
		}
		// A structured continuation of the SAME turn invalidates its earlier
		// completion signal; unseen completions from older turns stay unread.
		if ev.Kind != "completed" && ev.Kind != "unknown" && completionTurn == ev.Turn {
			completion = ""
			completionTurn = ""
		}
		_, err = tx.ExecContext(ctx, `UPDATE sessions SET attention_state = ?, attention_turn = ?, attention_sequence = ?, completion_revision = ?, completion_turn = ?, attention_event_hash = ?
    WHERE id = ?`, state, ev.Turn, ev.Sequence, completion, completionTurn, hash, id)
		return err
	})
}

// AcknowledgeCompletion is a compare-and-set on what the visible browser read.
// A new completion, native conversation, or generation racing it remains unread.
func (r *Repository) AcknowledgeCompletion(ctx context.Context, id string, generation int, revision string) error {
	if generation < 1 || !attentionID(revision, 32) {
		return hookcapture.ErrConflict
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ? AND removed_at IS NULL`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET completion_seen = ? WHERE id = ? AND generation = ? AND completion_revision = ? AND removed_at IS NULL`, revision, id, generation, revision)
		return err
	})
}

func (h *Handler) AcknowledgeCompletion(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Generation int    `json:"generation"`
		Revision   string `json:"revision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024))
	dec.DisallowUnknownFields()
	var trailing any
	if dec.Decode(&body) != nil || dec.Decode(&trailing) != io.EOF {
		h.err(w, 400, "bad_request", "invalid completion acknowledgment")
		return
	}
	err := h.sessions.AcknowledgeCompletion(req.Context(), req.PathValue("id"), body.Generation, body.Revision)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		h.err(w, 404, "not_found", "session not found")
	case errors.Is(err, hookcapture.ErrConflict):
		h.err(w, 400, "bad_request", "invalid completion acknowledgment")
	default:
		h.err(w, 500, "internal", "could not acknowledge completion")
	}
}

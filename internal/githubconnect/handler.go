package githubconnect

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"runtime"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/terminal"
)

// CSRFHeader carries the per-principal CSRF token. The trusted-proxy auth
// mode has no cookie CSRF to lean on, so POSTs are bound to the Origin
// allowlist and this HMAC exactly like the harness login handler.
const CSRFHeader = "X-GitHub-Connect-CSRF"

// Handler is the fixed-purpose HTTP surface for native GitHub connection.
// It owns no lifecycle state: the manager owns the attempt, native gh owns
// authentication, tmux owns process continuity.
type Handler struct {
	Manager *Manager
	Proxy   *terminal.Proxy
	Origins []string
	secret  [32]byte
}

// NewHandler builds the handler. The proxy must have been constructed with
// this manager as its Targets (see Manager.NewProxy).
func NewHandler(m *Manager, proxy *terminal.Proxy, origins []string) *Handler {
	h := &Handler{Manager: m, Proxy: proxy, Origins: append([]string(nil), origins...)}
	if _, err := rand.Read(h.secret[:]); err != nil {
		panic("github connect randomness unavailable")
	}
	return h
}

// NewProxy builds the terminal proxy over this manager's targets. The tmux
// client and inputgate are the same ones the manager holds — a separate
// Deck-owned transport, never a second WebSocket surface of the session
// proxy.
func (m *Manager) NewProxy(origins []string, log zerolog.Logger) *terminal.Proxy {
	return terminal.NewFor(m.tmux, m, lease.NewLocal(), m.gate, origins, log)
}

// Mount registers the endpoints behind the same authentication middleware
// as every other control API.
func (h *Handler) Mount(handle func(string, http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/github/connect/status", wrap(http.HandlerFunc(h.status)))
	handle("GET /api/github/connect/attempt", wrap(http.HandlerFunc(h.attempt)))
	handle("POST /api/github/connect/start", wrap(http.HandlerFunc(h.start)))
	handle("POST /api/github/connect/cancel", wrap(http.HandlerFunc(h.cancel)))
	handle("GET /api/github/connect/attempt/{id}/terminal", wrap(http.HandlerFunc(h.terminal)))
}

func (h *Handler) csrf(principal string) string {
	mac := hmac.New(sha256.New, h.secret[:])
	mac.Write([]byte(principal))
	return hex.EncodeToString(mac.Sum(nil))
}

// requirePost enforces the authenticated principal, the Origin allowlist,
// and the CSRF token on every mutating request.
func (h *Handler) requirePost(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	principal := auth.Principal(r.Context())
	if principal == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return "", false
	}
	originAllowed := false
	for _, origin := range h.Origins {
		if r.Header.Get("Origin") == origin {
			originAllowed = true
			break
		}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !originAllowed || !hmac.Equal([]byte(r.Header.Get(CSRFHeader)), []byte(h.csrf(principal))) ||
		err != nil || media != "application/json" {
		http.Error(w, "request origin not allowed", http.StatusForbidden)
		return "", false
	}
	return principal, true
}

// statusView is the safe projection of a native status read. It carries the
// credential-source classification, never a raw tokenSource path.
type statusView struct {
	Active          bool   `json:"active"`
	Outcome         string `json:"outcome"`
	Login           string `json:"login,omitempty"`
	GitProtocol     string `json:"git_protocol,omitempty"`
	State           string `json:"state,omitempty"`
	Source          string `json:"source,omitempty"`
	SourceWriteable bool   `json:"source_writeable,omitempty"`
	// Platform picks the install hint when gh is missing.
	Platform string `json:"platform,omitempty"`
}

func viewOf(res Result) statusView {
	v := statusView{Outcome: res.Outcome.String()}
	if res.Outcome == OutcomeMissingCLI {
		v.Platform = runtime.GOOS
	}
	if res.Status != nil {
		a := res.Status.Account
		v.Login, v.GitProtocol = a.Login, a.GitProtocol
		v.Active = a.Active
		v.State = a.State.String()
		v.Source = a.Source.String()
		v.SourceWriteable = a.SourceWriteable
	}
	return v
}

// status is a read-only native status read. It never starts a login.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if auth.Principal(r.Context()) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	res := h.Manager.Status(r.Context())
	writeJSON(w, http.StatusOK, viewOf(res))
}

// attempt returns the current attempt metadata (and the CSRF token for the
// principal's next POST), nothing else.
func (h *Handler) attempt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal := auth.Principal(r.Context())
	if principal == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	meta, ok := h.Manager.Current(r.Context(), principal)
	body := struct {
		CSRF    string           `json:"csrf"`
		Attempt *AttemptMetadata `json:"attempt"`
	}{CSRF: h.csrf(principal)}
	if ok {
		body.Attempt = &meta
	}
	writeJSON(w, http.StatusOK, body)
}

// start performs the explicit Connect. No body fields are accepted: the
// native argv is fixed and nothing about the request reaches it.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.requirePost(w, r)
	if !ok {
		return
	}
	meta, err := h.Manager.Start(r.Context(), principal)
	if err != nil {
		writeStartError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		http.Error(w, "authentication required", http.StatusUnauthorized)
	case errors.Is(err, ErrAlreadyConnected),
		errors.Is(err, ErrExistingAccount),
		errors.Is(err, ErrAttemptInProgress),
		errors.Is(err, ErrAttemptBudget):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrNotOwner):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, ErrAttemptNotRunning):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, ErrMissingCLI):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, "cannot start github connect", http.StatusInternalServerError)
	}
}

// cancel performs the explicit cancellation of the caller's own attempt.
func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.requirePost(w, r)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	err := h.Manager.Cancel(r.Context(), principal, body.ID)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownAttempt):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrNotOwner):
			http.Error(w, err.Error(), http.StatusForbidden)
		case errors.Is(err, ErrIdentityChanged), errors.Is(err, ErrAttemptNotRunning):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrCancelUncertain):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case errors.Is(err, ErrUnauthenticated):
			http.Error(w, "authentication required", http.StatusUnauthorized)
		default:
			http.Error(w, "cannot cancel github connect", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// terminal serves the login attempt's pane. The owner is verified before
// the proxy sees the request; the served context joins the request to the
// attempt's cancellation because the proxy has no Close of its own — a
// browser leaving ends only that attachment, while cancelling the attempt
// revokes every output attachment.
func (h *Handler) terminal(w http.ResponseWriter, r *http.Request) {
	principal := auth.Principal(r.Context())
	if principal == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	actx, err := h.Manager.AttachContext(principal, id)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotOwner):
			http.Error(w, "attempt owned by another user", http.StatusForbidden)
		default:
			http.Error(w, "no current attempt", http.StatusNotFound)
		}
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(actx, cancel)
	defer stop()
	h.Proxy.Handle(w, r.WithContext(ctx))
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

// compile-time interface checks against the concrete dependencies.
var _ terminal.Targets = (*Manager)(nil)

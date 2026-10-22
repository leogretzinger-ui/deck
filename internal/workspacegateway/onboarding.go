package workspacegateway

import (
	"context"
	"net/http"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

// PlainOperations observes the fixed private runtime before dispatch. Prepare
// may reserve a new start only after the previous operation is terminal and
// native observation confirms stopped. Execute revalidates its typed permit;
// Status only observes and cannot replay uncertain effects.
type PlainOperations interface {
	Prepare(context.Context, auth.Login) (workspacekeys.PlainStartPermit, error)
	Execute(context.Context, auth.Login, workspacekeys.PlainStartPermit) error
	Status(context.Context, auth.Login, string) (OperationState, error)
}

func (h *Handler) onboarding(w http.ResponseWriter, r *http.Request) {
	state, err := h.keys.Onboarding(r.Context())
	if err != nil {
		keyError(w, err)
		return
	}
	respond(w, http.StatusOK, state)
}

func (h *Handler) selectProtection(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protection workspacekeys.Protection `json:"protection"`
	}
	if !decode(w, r, &request) {
		return
	}
	state, err := h.keys.SelectProtection(r.Context(), actor(r), request.Protection)
	if err != nil {
		keyError(w, err)
		return
	}
	respond(w, http.StatusOK, state)
}

func (h *Handler) plainStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OperationID string `json:"operationId,omitempty"`
	}
	if !decode(w, r, &request) {
		return
	}
	login, _ := auth.CurrentLogin(r.Context())
	state, err := h.keys.State(r.Context(), login)
	if err != nil {
		keyError(w, err)
		return
	}
	if !state.OwnerBound || state.Onboarding.Protection != workspacekeys.ProtectionNone {
		keyError(w, workspacekeys.ErrDenied)
		return
	}
	if h.options.PlainOperations == nil {
		fail(w, http.StatusServiceUnavailable, "controller-unavailable")
		return
	}
	var permit workspacekeys.PlainStartPermit
	if request.OperationID == "" {
		permit, err = h.options.PlainOperations.Prepare(r.Context(), login)
	} else {
		permit, err = h.keys.AuthorizePlainStart(r.Context(), login, request.OperationID)
	}
	if err == nil {
		err = h.keys.ValidatePlainStart(r.Context(), login, permit)
	}
	if err != nil {
		keyError(w, err)
		return
	}
	status, code := "accepted", http.StatusOK
	if err = h.options.PlainOperations.Execute(r.Context(), login, permit); err != nil {
		status, code = "unconfirmed", http.StatusAccepted
	}
	respond(w, code, struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}{permit.ID(), status})
}

func (h *Handler) plainStatus(w http.ResponseWriter, r *http.Request) {
	login, _ := auth.CurrentLogin(r.Context())
	id := r.PathValue("id")
	if _, err := h.keys.PlainOperation(r.Context(), login, id); err != nil {
		fail(w, http.StatusNotFound, "operation-unavailable")
		return
	}
	if h.options.PlainOperations == nil {
		fail(w, http.StatusServiceUnavailable, "controller-unavailable")
		return
	}
	status, err := h.options.PlainOperations.Status(r.Context(), login, id)
	if err != nil || status.ID != id || !validRuntimeState(status.RuntimeState) {
		fail(w, http.StatusServiceUnavailable, "operation-unavailable")
		return
	}
	respond(w, http.StatusOK, status)
}

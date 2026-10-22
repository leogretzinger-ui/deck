package workspacekeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/noisefloor-oss/deck/internal/auth"
)

// Actor comes from the gateway's verified auth context, never decoded from a request body.
type Actor struct {
	Login           auth.Login
	enrollmentToken string
}

func NewActor(login auth.Login, enrollmentToken string) Actor {
	return Actor{Login: login, enrollmentToken: enrollmentToken}
}
func (Actor) String() string   { return "workspacekeys.Actor" }
func (Actor) GoString() string { return "workspacekeys.Actor" }

type MethodRef struct {
	ID       string `json:"id"`
	Revision uint32 `json:"revision"`
}

// PreparationAuthorizer checks a current method-backed browser grant inside the registry transaction.
// Its returned issuing method must still be active at the same revision.
type PreparationAuthorizer interface {
	RequirePreparationTx(context.Context, *sql.Tx, auth.Login, string, string) (MethodRef, error)
}

type Config struct {
	EncryptionPolicy EncryptionPolicy
	InstanceID       string
	RPID             string
	Origin           string
	Now              func() time.Time
	Preparation      PreparationAuthorizer
}

type EnrollmentReceipt struct {
	Revision  uint32    `json:"revision"`
	ExpiresAt time.Time `json:"expiresAt"`
	token     string
}

func (r EnrollmentReceipt) Token() string  { return r.token }
func (EnrollmentReceipt) String() string   { return "workspacekeys.EnrollmentReceipt" }
func (EnrollmentReceipt) GoString() string { return "workspacekeys.EnrollmentReceipt" }

type State struct {
	Onboarding            OnboardingState `json:"onboarding"`
	PlainOperationID      string          `json:"plainOperationId,omitempty"`
	OwnerBound            bool            `json:"ownerBound"`
	BootstrapClosed       bool            `json:"bootstrapClosed"`
	Initialized           bool            `json:"initialized"`
	EnrollmentRevision    uint32          `json:"enrollmentRevision"`
	Commitment            string          `json:"commitment,omitempty"`
	RuntimeEpoch          string          `json:"runtimeEpoch"`
	InitializingOperation string          `json:"initializingOperation,omitempty"`
}

type Method struct {
	MethodRef
	Kind           Kind       `json:"kind"`
	Label          string     `json:"label"`
	Status         string     `json:"status"`
	Salt           string     `json:"salt"`
	Envelope       *Envelope  `json:"envelope,omitempty"`
	EnvelopeDigest string     `json:"envelopeDigest,omitempty"`
	VerifiedAt     *time.Time `json:"verifiedAt,omitempty"`
	CredentialID   string     `json:"credentialId,omitempty"`
}

type PreparedMethod struct {
	Method       Method                       `json:"method"`
	Registration *protocol.CredentialCreation `json:"registration,omitempty"`
}

type Operation string

const (
	VerifyMethod         Operation = "verify-method"
	ResumeEnrollment     Operation = "resume-enrollment"
	ResumeInitialization Operation = "resume-initialization"
	Initialize           Operation = "initialize"
	AuthorizeBrowser     Operation = "authorize-browser"
	Unlock               Operation = "unlock"
	AddMethod            Operation = "add-method"
	ReplaceMethod        Operation = "replace-method"
	RemoveMethod         Operation = "remove-method"
	RenameMethod         Operation = "rename-method"
	Lock                 Operation = "lock"
)

// Action has only nonsecret control fields. The registry persists its exact encoding.
type Action struct {
	Operation   Operation  `json:"operation"`
	Method      MethodRef  `json:"method"`
	Target      *MethodRef `json:"target,omitempty"`
	Replacement *MethodRef `json:"replacement,omitempty"`
	Recovery    *MethodRef `json:"recovery,omitempty"`
	Label       string     `json:"label,omitempty"`
}

type Challenge struct {
	ID          string                        `json:"id"`
	OperationID string                        `json:"operationId"`
	Challenge   string                        `json:"challenge"`
	ExpiresAt   time.Time                     `json:"expiresAt"`
	Assertion   *protocol.CredentialAssertion `json:"assertion,omitempty"`
}

// AuthorizedOperation proves one registry decision; it does not grant browser data access.
type AuthorizedOperation struct {
	ID           string             `json:"id"`
	Action       Action             `json:"action"`
	Payload      json.RawMessage    `json:"payload"`
	RuntimeEpoch string             `json:"runtimeEpoch"`
	Receipt      *EnrollmentReceipt `json:"-"`
	registry     *Registry
	proof        *browserProof
	execution    *executionProof
}

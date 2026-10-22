// Package workspacekeys verifies browser key proofs and records public workspace key metadata.
package workspacekeys

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
)

var (
	ErrInvalid  = errors.New("workspacekeys: invalid request")
	ErrDenied   = errors.New("workspacekeys: authorization refused")
	ErrConflict = errors.New("workspacekeys: state changed")
	ErrNotFound = errors.New("workspacekeys: record not found")
	identifier  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type Kind string

const (
	Passkey  Kind = "passkey"
	Password Kind = "password"
	Recovery Kind = "recovery"
)

func (k Kind) valid() bool { return k == Passkey || k == Password || k == Recovery }

type Envelope struct {
	Version        int     `json:"version"`
	InstanceID     string  `json:"instanceId"`
	MethodID       string  `json:"methodId"`
	Revision       uint32  `json:"revision"`
	Kind           Kind    `json:"kind"`
	Salt           string  `json:"salt"`
	Nonce          string  `json:"nonce"`
	Ciphertext     string  `json:"ciphertext"`
	ProofPublicKey string  `json:"proofPublicKey"`
	Iterations     *uint32 `json:"iterations,omitempty"`
}

// DecodeEnvelope rejects duplicate fields rather than depending on a parser's last-value rule.
func DecodeEnvelope(data []byte) (Envelope, error) {
	var result Envelope
	if len(data) == 0 || len(data) > 16384 {
		return result, ErrInvalid
	}
	if err := rejectDuplicateFields(data); err != nil {
		return result, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Envelope{}, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Envelope{}, ErrInvalid
	}
	expected := []string{"version", "instanceId", "methodId", "revision", "kind", "salt", "nonce", "ciphertext", "proofPublicKey"}
	if result.Kind == Password {
		expected = append(expected, "iterations")
	}
	if len(fields) != len(expected) {
		return Envelope{}, ErrInvalid
	}
	for _, field := range expected {
		if _, present := fields[field]; !present {
			return Envelope{}, ErrInvalid
		}
	}
	if err := result.Validate(); err != nil {
		return Envelope{}, err
	}
	return result, nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		field, err := decoder.Token()
		name, ok := field.(string)
		if err != nil || !ok || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrInvalid
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (e Envelope) Validate() error {
	if e.Version != 1 || !identifier.MatchString(e.InstanceID) || !identifier.MatchString(e.MethodID) || e.Revision == 0 || !e.Kind.valid() {
		return ErrInvalid
	}
	if e.Kind == Password {
		if e.Iterations == nil || *e.Iterations != 600000 {
			return ErrInvalid
		}
	} else if e.Iterations != nil {
		return ErrInvalid
	}
	if _, err := decodeBytes(e.Salt, 32, 32); err != nil {
		return err
	}
	if _, err := decodeBytes(e.Nonce, 12, 12); err != nil {
		return err
	}
	if _, err := decodeBytes(e.Ciphertext, 17, 4096); err != nil {
		return err
	}
	_, err := proofKey(e.ProofPublicKey)
	return err
}

func decodeBytes(value string, minimum, maximum int) ([]byte, error) {
	if len(value) > (maximum*4+2)/3 {
		return nil, ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minimum || len(decoded) > maximum || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, ErrInvalid
	}
	return decoded, nil
}

func proofKey(encoded string) (*ecdsa.PublicKey, error) {
	der, err := decodeBytes(encoded, 1, 1024)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	key, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || key.Curve != elliptic.P256() || !key.IsOnCurve(key.X, key.Y) {
		return nil, ErrInvalid
	}
	return key, nil
}

// VerifyProof accepts WebCrypto's raw P-256 r||s encoding, not WebAuthn's DER signature.
func VerifyProof(publicKey string, challenge []byte, signature string) error {
	if len(challenge) != 32 {
		return ErrInvalid
	}
	key, err := proofKey(publicKey)
	if err != nil {
		return err
	}
	raw, err := decodeBytes(signature, 64, 64)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(challenge)
	if !ecdsa.Verify(key, hash[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])) {
		return ErrDenied
	}
	return nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

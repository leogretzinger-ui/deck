package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	TransientUnitPropertiesSchemaV2 = "noisedeck.transient-unit-properties.v2"
	transientUnitNameDomainV1       = "noisedeck.transient-unit-name.v1"
	transientUnitNamePrefix         = "noisedeck-confined-"
	transientUnitNameSuffix         = ".service"
	transientUnitStopTimeoutUSec    = uint64(30000000)
	transientUnitSlice              = "noisedeck-confined.slice"
	transientLauncherPath           = "/usr/local/libexec/noise/noise-deck"
	transientLauncherMode           = "confined-launcher"
	transientLauncherRunIDFlag      = "--run-id"
	transientNullStream             = "null"
)

// D-Bus setter signatures admitted by the closed lifecycle vector.
const (
	transientSigString    = "s"
	transientSigBool      = "b"
	transientSigUint64    = "t"
	transientSigExecStart = "a(sasb)"
)

var (
	ErrInvalidTransientUnitProperties  = errors.New("invalid confined transient unit properties")
	ErrTransientUnitPropertiesEncoding = errors.New("confined transient unit properties are not canonical")
	ErrTransientUnitPropertiesDigest   = errors.New("confined transient unit properties digest mismatch")
	ErrTransientUnitPropertiesBinding  = errors.New("confined transient unit properties do not match run binding")
)

// TransientUnitExecCommand mirrors one systemd ExecStart tuple, D-Bus
// signature a(sasb): the executable path, the full argument vector, and the
// ignore-failure flag. The supervisor owns every element; nothing here is
// caller input.
type TransientUnitExecCommand struct {
	Path          string   `json:"path"`
	Argv          []string `json:"argv"`
	IgnoreFailure bool     `json:"ignore_failure"`
}

// TransientUnitProperty is one typed item in the closed, ordered lifecycle
// property vector for confined-linux-v1. Signature selects the D-Bus setter
// type; exactly one value arm is set, and the canonical bytes never stringify
// a boolean, uint64, or ExecStart command.
type TransientUnitProperty struct {
	Name      string
	Signature string
	String    string
	Boolean   bool
	Uint      uint64
	ExecStart []TransientUnitExecCommand
}

// TransientUnitProperties binds the deterministic service identity and its
// closed lifecycle properties to one supervisor run and sandbox generation.
// It defines inert authority bytes only; it does not create or inspect a unit.
type TransientUnitProperties struct {
	Schema            string                  `json:"schema"`
	Profile           ConfinementProfile      `json:"profile"`
	RunID             string                  `json:"run_id"`
	SandboxGeneration string                  `json:"sandbox_generation"`
	UnitName          string                  `json:"unit_name"`
	Properties        []TransientUnitProperty `json:"properties"`
}

// TransientUnitPropertiesRevision is the exact identity retained beside a run
// authority row. UnitName and Digest correspond to the run store fields.
type TransientUnitPropertiesRevision struct {
	UnitName      string
	Digest        string
	CanonicalJSON string
	Properties    TransientUnitProperties
}

// TransientUnitPropertiesBinding contains the supervisor-issued identities
// that select one deterministic unit contract.
type TransientUnitPropertiesBinding struct {
	RunID             string
	SandboxGeneration string
}

// DeriveTransientUnitProperties constructs the only unit identity and closed
// lifecycle property vector admitted by confined-linux-v1 for a run.
func DeriveTransientUnitProperties(binding TransientUnitPropertiesBinding) (*TransientUnitPropertiesRevision, error) {
	if !validTransientUnitBinding(binding) {
		return nil, ErrInvalidTransientUnitProperties
	}
	unitName := deriveTransientUnitName(binding)
	properties := TransientUnitProperties{
		Schema:            TransientUnitPropertiesSchemaV2,
		Profile:           ProfileConfinedLinuxV1,
		RunID:             binding.RunID,
		SandboxGeneration: binding.SandboxGeneration,
		UnitName:          unitName,
		Properties:        requiredTransientUnitProperties(binding.RunID),
	}
	raw, err := json.Marshal(properties)
	if err != nil {
		return nil, ErrInvalidTransientUnitProperties
	}
	digest := sha256.Sum256(raw)
	return &TransientUnitPropertiesRevision{
		UnitName:      unitName,
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Properties:    properties,
	}, nil
}

// VerifyTransientUnitProperties rejects any caller-selected unit identity or
// property shape and compares the canonical bytes with the protected run
// binding. A mismatch fails closed before a service-manager operation.
func VerifyTransientUnitProperties(
	unitName string,
	digest string,
	canonicalJSON string,
	binding TransientUnitPropertiesBinding,
) (*TransientUnitPropertiesRevision, error) {
	if !validDeterministicUnitName(unitName) {
		return nil, ErrInvalidTransientUnitProperties
	}
	if !isLowerSHA256(digest) {
		return nil, ErrTransientUnitPropertiesDigest
	}
	if !validTransientUnitBinding(binding) {
		return nil, ErrInvalidTransientUnitProperties
	}

	var properties TransientUnitProperties
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&properties); err != nil {
		return nil, ErrTransientUnitPropertiesEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrTransientUnitPropertiesEncoding
	}
	if !validTransientUnitProperties(properties) {
		return nil, ErrInvalidTransientUnitProperties
	}
	raw, err := json.Marshal(properties)
	if err != nil || string(raw) != canonicalJSON {
		return nil, ErrTransientUnitPropertiesEncoding
	}
	actualDigest := sha256.Sum256(raw)
	if hex.EncodeToString(actualDigest[:]) != digest {
		return nil, ErrTransientUnitPropertiesDigest
	}

	expected, err := DeriveTransientUnitProperties(binding)
	if err != nil {
		return nil, err
	}
	if unitName != expected.UnitName ||
		properties.RunID != binding.RunID ||
		properties.SandboxGeneration != binding.SandboxGeneration ||
		canonicalJSON != expected.CanonicalJSON {
		return nil, ErrTransientUnitPropertiesBinding
	}
	return expected, nil
}

// MarshalJSON emits the closed name/signature/value triple, with value as the
// exact typed JSON for the active arm. The signature is the only
// discriminator; a nonzero value in any inactive arm fails closed so the
// signature cannot be bypassed, and every arm carries its native D-Bus type.
func (p TransientUnitProperty) MarshalJSON() ([]byte, error) {
	if !transientPropertyArmsClean(p) {
		return nil, ErrInvalidTransientUnitProperties
	}
	value, err := p.marshalValue()
	if err != nil {
		return nil, err
	}
	name, err := json.Marshal(p.Name)
	if err != nil {
		return nil, err
	}
	signature, err := json.Marshal(p.Signature)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"name":`)
	buf.Write(name)
	buf.WriteString(`,"signature":`)
	buf.Write(signature)
	buf.WriteString(`,"value":`)
	buf.Write(value)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (p TransientUnitProperty) marshalValue() ([]byte, error) {
	switch p.Signature {
	case transientSigString:
		return json.Marshal(p.String)
	case transientSigBool:
		return json.Marshal(p.Boolean)
	case transientSigUint64:
		return json.Marshal(p.Uint)
	case transientSigExecStart:
		return json.Marshal(p.ExecStart)
	default:
		return nil, ErrInvalidTransientUnitProperties
	}
}

// UnmarshalJSON decodes the closed name/signature/value triple and rejects an
// unknown signature, a value whose JSON type does not match the signature, or
// trailing content. The receiver is zeroed at entry so decoding into a reused
// value clears every inactive arm. Each arm is decoded with unknown-field
// rejection so a hostile value cannot smuggle extra bytes.
func (p *TransientUnitProperty) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Signature string          `json:"signature"`
		Value     json.RawMessage `json:"value"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if err := requireJSONEOF(dec); err != nil {
		return err
	}
	*p = TransientUnitProperty{}
	p.Name = raw.Name
	p.Signature = raw.Signature
	return p.decodeValue(raw.Value)
}

func (p *TransientUnitProperty) decodeValue(raw json.RawMessage) error {
	switch p.Signature {
	case transientSigString:
		var v string
		if err := decodeTransientValue(raw, &v); err != nil {
			return err
		}
		p.String = v
	case transientSigBool:
		var v bool
		if err := decodeTransientValue(raw, &v); err != nil {
			return err
		}
		p.Boolean = v
	case transientSigUint64:
		var v uint64
		if err := decodeTransientValue(raw, &v); err != nil {
			return err
		}
		p.Uint = v
	case transientSigExecStart:
		var v []TransientUnitExecCommand
		if err := decodeTransientValue(raw, &v); err != nil {
			return err
		}
		p.ExecStart = v
	default:
		return ErrInvalidTransientUnitProperties
	}
	return nil
}

func decodeTransientValue(raw json.RawMessage, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

func validTransientUnitBinding(binding TransientUnitPropertiesBinding) bool {
	return isCanonicalUUIDv4(binding.RunID) && isCanonicalUUIDv4(binding.SandboxGeneration)
}

func deriveTransientUnitName(binding TransientUnitPropertiesBinding) string {
	identity := transientUnitNameDomainV1 + "\x00" + binding.RunID + "\x00" + binding.SandboxGeneration
	digest := sha256.Sum256([]byte(identity))
	return transientUnitNamePrefix + hex.EncodeToString(digest[:]) + transientUnitNameSuffix
}

func requiredTransientUnitProperties(runID string) []TransientUnitProperty {
	argv := []string{transientLauncherPath, transientLauncherMode, transientLauncherRunIDFlag, runID}
	return []TransientUnitProperty{
		{Name: "Type", Signature: transientSigString, String: "exec"},
		{Name: "ExitType", Signature: transientSigString, String: "cgroup"},
		{Name: "RemainAfterExit", Signature: transientSigBool, Boolean: true},
		{Name: "CollectMode", Signature: transientSigString, String: "inactive"},
		{Name: "Restart", Signature: transientSigString, String: "no"},
		{Name: "KillMode", Signature: transientSigString, String: "control-group"},
		{Name: "SendSIGKILL", Signature: transientSigBool, Boolean: true},
		{Name: "TimeoutStopUSec", Signature: transientSigUint64, Uint: transientUnitStopTimeoutUSec},
		{Name: "Slice", Signature: transientSigString, String: transientUnitSlice},
		{Name: "StandardInput", Signature: transientSigString, String: transientNullStream},
		{Name: "StandardOutput", Signature: transientSigString, String: transientNullStream},
		{Name: "StandardError", Signature: transientSigString, String: transientNullStream},
		{Name: "ExecStart", Signature: transientSigExecStart, ExecStart: []TransientUnitExecCommand{
			{Path: transientLauncherPath, Argv: argv, IgnoreFailure: false},
		}},
	}
}

func validTransientUnitProperties(properties TransientUnitProperties) bool {
	if properties.Schema != TransientUnitPropertiesSchemaV2 ||
		properties.Profile != ProfileConfinedLinuxV1 ||
		!isCanonicalUUIDv4(properties.RunID) ||
		!isCanonicalUUIDv4(properties.SandboxGeneration) {
		return false
	}
	binding := TransientUnitPropertiesBinding{
		RunID:             properties.RunID,
		SandboxGeneration: properties.SandboxGeneration,
	}
	if properties.UnitName != deriveTransientUnitName(binding) {
		return false
	}
	required := requiredTransientUnitProperties(properties.RunID)
	if len(properties.Properties) != len(required) {
		return false
	}
	for i := range required {
		if !transientPropertyEqual(properties.Properties[i], required[i]) {
			return false
		}
	}
	return true
}

// transientPropertyArmsClean reports whether only the active arm for the
// signature is populated. A nonzero value in any inactive arm would let the
// signature discriminator be bypassed, so canonicalization and equality treat
// such a value as invalid. The active arm may itself hold its native zero
// value; an unknown signature is not clean.
func transientPropertyArmsClean(p TransientUnitProperty) bool {
	switch p.Signature {
	case transientSigString:
		return !p.Boolean && p.Uint == 0 && len(p.ExecStart) == 0
	case transientSigBool:
		return p.String == "" && p.Uint == 0 && len(p.ExecStart) == 0
	case transientSigUint64:
		return p.String == "" && !p.Boolean && len(p.ExecStart) == 0
	case transientSigExecStart:
		return p.String == "" && !p.Boolean && p.Uint == 0
	default:
		return false
	}
}

func transientPropertyEqual(actual, required TransientUnitProperty) bool {
	if actual.Name != required.Name || actual.Signature != required.Signature {
		return false
	}
	if !transientPropertyArmsClean(actual) || !transientPropertyArmsClean(required) {
		return false
	}
	switch required.Signature {
	case transientSigString:
		return actual.String == required.String
	case transientSigBool:
		return actual.Boolean == required.Boolean
	case transientSigUint64:
		return actual.Uint == required.Uint
	case transientSigExecStart:
		return execCommandsEqual(actual.ExecStart, required.ExecStart)
	default:
		return false
	}
}

func execCommandsEqual(actual, required []TransientUnitExecCommand) bool {
	if len(actual) != len(required) {
		return false
	}
	for i := range required {
		if actual[i].Path != required[i].Path ||
			actual[i].IgnoreFailure != required[i].IgnoreFailure ||
			len(actual[i].Argv) != len(required[i].Argv) {
			return false
		}
		for j := range required[i].Argv {
			if actual[i].Argv[j] != required[i].Argv[j] {
				return false
			}
		}
	}
	return true
}

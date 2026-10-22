package workspacekeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// This test authenticator exercises actual CBOR, origin/RP binding, signatures and UV
// verification. It makes no claim about a browser's or password manager's PRF support.
type testAuthenticator struct {
	key     *ecdsa.PrivateKey
	id      []byte
	backup  bool
	counter uint32
}

func authenticator(t *testing.T) *testAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &testAuthenticator{key: key, id: randomBytes(), backup: true}
}
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (a *testAuthenticator) authData(rpid string, uv, registration bool) []byte {
	hash := sha256.Sum256([]byte(rpid))
	data := append([]byte(nil), hash[:]...)
	flags := byte(1)
	if uv {
		flags |= 4
	}
	if registration {
		flags |= 64
	}
	if a.backup {
		flags |= 8
	}
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, a.counter)
	if registration {
		data = append(data, make([]byte, 16)...)
		data = binary.BigEndian.AppendUint16(data, uint16(len(a.id)))
		data = append(data, a.id...)
		x, y := make([]byte, 32), make([]byte, 32)
		a.key.X.FillBytes(x)
		a.key.Y.FillBytes(y)
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		data = append(data, cose...)
	}
	return data
}
func (a *testAuthenticator) registration(t *testing.T, prepared PreparedMethod, origin, rpid string, uv, prf bool) []byte {
	t.Helper()
	client := jsonBytes(t, map[string]any{"type": "webauthn.create", "challenge": b64(prepared.Registration.Response.Challenge), "origin": origin})
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(rpid, uv, true)})
	if err != nil {
		t.Fatal(err)
	}
	return jsonBytes(t, map[string]any{"id": b64(a.id), "rawId": b64(a.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation), "transports": []string{"internal"}}, "clientExtensionResults": map[string]any{"prf": map[string]any{"enabled": prf}, "credProps": map[string]any{"rk": true}}})
}
func (a *testAuthenticator) assertion(t *testing.T, ch Challenge, origin, rpid string, uv bool) []byte {
	t.Helper()
	client := jsonBytes(t, map[string]any{"type": "webauthn.get", "challenge": ch.Challenge, "origin": origin})
	authData := a.authData(rpid, uv, false)
	clientHash := sha256.Sum256(client)
	signed := append(append([]byte(nil), authData...), clientHash[:]...)
	hash := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return jsonBytes(t, map[string]any{"id": b64(a.id), "rawId": b64(a.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64(client), "authenticatorData": b64(authData), "signature": b64(signature), "userHandle": nil}, "clientExtensionResults": map[string]any{}})
}
func (f *fixture) passkey() (methodFixture, *testAuthenticator) {
	f.t.Helper()
	prepared, err := f.r.PrepareMethod(f.ctx, f.actor, Passkey, "Synced test passkey")
	if err != nil {
		f.t.Fatal(err)
	}
	if prepared.Registration.Response.AuthenticatorSelection.UserVerification != protocol.VerificationRequired || prepared.Registration.Response.AuthenticatorSelection.ResidentKey != protocol.ResidentKeyRequirementRequired {
		f.t.Fatal("registration weakened selection")
	}
	m := f.envelopeFor(prepared.Method)
	a := authenticator(f.t)
	if err = f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, a.registration(f.t, prepared, "https://deck.example", "deck.example", true, true), f.commitment); err != nil {
		f.t.Fatal("valid registration failed", err)
	}
	return m, a
}

func TestWebAuthnRegistrationRequiresOriginRPAndUV(t *testing.T) {
	for _, mode := range []string{"origin", "rp", "uv", "no-prf", "wrong-challenge"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			p, err := f.r.PrepareMethod(f.ctx, f.actor, Passkey, "Passkey")
			if err != nil {
				t.Fatal(err)
			}
			m := f.envelopeFor(p.Method)
			a := authenticator(t)
			origin, rp, uv, prf := "https://deck.example", "deck.example", true, true
			switch mode {
			case "origin":
				origin = "https://evil.example"
			case "rp":
				rp = "evil.example"
			case "uv":
				uv = false
			case "no-prf":
				prf = false
			case "wrong-challenge":
				p.Registration.Response.Challenge = randomBytes()
			}
			err = f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, a.registration(t, p, origin, rp, uv, prf), f.commitment)
			refused(t, err)
		})
	}
}

func TestPasskeyProofRequiresFreshVerifiedAssertion(t *testing.T) {
	f := newFixture(t)
	m, a := f.passkey()
	for _, mode := range []string{"missing", "origin", "rp", "uv", "challenge", "credential", "signature", "prf-secret"} {
		t.Run(mode, func(t *testing.T) {
			ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
			if err != nil {
				t.Fatal(err)
			}
			if ch.Assertion == nil || ch.Assertion.Response.UserVerification != protocol.VerificationRequired || ch.Assertion.Response.RelyingPartyID != "deck.example" || len(ch.Assertion.Response.AllowedCredentials) != 1 {
				t.Fatal("assertion options not bound")
			}
			proof := sign(t, m.key, ch)
			origin, rp, uv := "https://deck.example", "deck.example", true
			modified := ch
			switch mode {
			case "origin":
				origin = "https://evil.example"
			case "rp":
				rp = "evil.example"
			case "uv":
				uv = false
			case "challenge":
				modified.Challenge = randomToken()
			}
			assertion := a.assertion(t, modified, origin, rp, uv)
			if mode == "missing" {
				assertion = nil
			}
			if mode == "credential" || mode == "signature" || mode == "prf-secret" {
				var data map[string]any
				json.Unmarshal(assertion, &data)
				switch mode {
				case "credential":
					data["id"] = randomToken()
					data["rawId"] = data["id"]
				case "signature":
					data["response"].(map[string]any)["signature"] = b64(make([]byte, 64))
				case "prf-secret":
					data["clientExtensionResults"] = map[string]any{"prf": map[string]any{"results": map[string]any{"first": randomToken()}}}
				}
				assertion = jsonBytes(t, data)
			}
			_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, proof, assertion)
			refused(t, err)
		})
	}
	// A synced authenticator can retain counter zero; freshness comes from the nonce.
	for range 2 {
		ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
		if err != nil {
			t.Fatal(err)
		}
		assertion := a.assertion(t, ch, "https://deck.example", "deck.example", true)
		_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), assertion)
		if err != nil {
			t.Fatal("valid synced assertion refused", err)
		}
		next, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.r.ConsumeProof(f.ctx, f.actor, next.ID, sign(t, m.key, next), assertion)
		refused(t, err)
	}
}

func TestDeviceBoundPasskeyCounterRollback(t *testing.T) {
	f := newFixture(t)
	p, err := f.r.PrepareMethod(f.ctx, f.actor, Passkey, "Device test passkey")
	if err != nil {
		t.Fatal(err)
	}
	m := f.envelopeFor(p.Method)
	a := authenticator(t)
	a.backup = false
	a.counter = 1
	if err = f.r.StoreEnvelope(f.ctx, f.actor, m.method.ID, m.encoded, a.registration(t, p, "https://deck.example", "deck.example", true, true), f.commitment); err != nil {
		t.Fatal(err)
	}
	ch, err := f.r.BeginChallenge(f.ctx, f.actor, Action{Operation: VerifyMethod, Method: m.method.MethodRef})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), a.assertion(t, ch, "https://deck.example", "deck.example", true))
	refused(t, err)
	a.counter = 2
	_, err = f.r.ConsumeProof(f.ctx, f.actor, ch.ID, sign(t, m.key, ch), a.assertion(t, ch, "https://deck.example", "deck.example", true))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicCredentialBoundaryRejectsSecretAndUnknownFields(t *testing.T) {
	for _, value := range []string{
		`{"response":{},"clientExtensionResults":{"prf":{"results":{"first":"secret"}}}}`,
		`{"response":{},"clientExtensionResults":{"largeBlob":{"blob":"secret"}}}`,
		`{"response":{},"clientExtensionResults":{"prf":{"enabled":true,"enabled":false}}}`,
		`{"response":{},"clientExtensionResults":{"prf":{"enabled":null}}}`,
		`{"response":{"privateKey":"secret"}}`, `{"response":{"clientDataJSON":"a","clientDataJSON":"b"}}`,
		`{"response":{},"secret":"not allowed"}`,
	} {
		if err := publicWebAuthnJSON([]byte(value), false); err == nil {
			t.Fatal("unbounded credential accepted")
		}
	}
}

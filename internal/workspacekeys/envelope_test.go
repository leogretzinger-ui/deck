package workspacekeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestWebCryptoProof(t *testing.T) {
	// Public fixture produced by Node's WebCrypto implementation.
	key := "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEkWp_dajJUewNCmtAonXwILaHaCyJh-GDfLr5EhcuvwoDefehDyrgpAFk232Do8wQnfxi4mqf7JmHfBCYPPir0w"
	challenge, _ := base64.RawURLEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
	signature := "YqlTPiy12r12mOari-KthP95KmfO5BK3dTTfv54b-EHtf_mC_VOgqM2-EnxkEyZSRs0y9BwqAFa_CIkxz3Lvkg"
	if err := VerifyProof(key, challenge, signature); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{signature[:len(signature)-1], signature + "=", "_x", strings.Repeat("A", 86)} {
		if err := VerifyProof(key, challenge, invalid); err == nil {
			t.Fatal("accepted invalid signature")
		}
	}
	challenge[0] ^= 1
	if err := VerifyProof(key, challenge, signature); err == nil {
		t.Fatal("accepted different challenge")
	}
	if err := VerifyProof(key, challenge[:31], signature); err == nil {
		t.Fatal("accepted short challenge")
	}
}

func testEnvelope(t *testing.T, kind Kind) (Envelope, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	envelope := Envelope{Version: 1, InstanceID: "instance", MethodID: "method", Revision: 1, Kind: kind,
		Salt: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 12)),
		Ciphertext: base64.RawURLEncoding.EncodeToString(make([]byte, 64)), ProofPublicKey: base64.RawURLEncoding.EncodeToString(der)}
	if kind == Password {
		count := uint32(600000)
		envelope.Iterations = &count
	}
	return envelope, key
}

func TestEnvelopeBounds(t *testing.T) {
	envelope, _ := testEnvelope(t, Password)
	encoded, _ := json.Marshal(envelope)
	if _, err := DecodeEnvelope(encoded); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(string(encoded), `"version":1`, `"version":1,"version":2`, 1),
		strings.Replace(string(encoded), `"version":1`, `"version":2`, 1),
		strings.Replace(string(encoded), `"iterations":600000`, `"iterations":1`, 1),
		strings.Replace(string(encoded), `"revision":1`, `"revision":4294967296`, 1),
		strings.Replace(string(encoded), `"revision":1`, `"revision":null`, 1),
		strings.Replace(string(encoded), `"version":1`, `"version":1,"secret":"forbidden"`, 1),
		strings.Replace(string(encoded), `"iterations":600000`, `"iterations":null`, 1),
		strings.Replace(string(encoded), `"instanceId":`, `"InstanceId":`, 1),
		string(encoded) + "{}", strings.Repeat(" ", 16385),
	} {
		if _, err := DecodeEnvelope([]byte(invalid)); err == nil {
			t.Fatal("accepted malformed envelope")
		}
	}
	recovery, _ := testEnvelope(t, Recovery)
	recoveryJSON, _ := json.Marshal(recovery)
	recoveryJSON = append(recoveryJSON[:len(recoveryJSON)-1], []byte(`,"iterations":null}`)...)
	if _, err := DecodeEnvelope(recoveryJSON); err == nil {
		t.Fatal("accepted forbidden recovery KDF field")
	}
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.Salt += "=" }, func(e *Envelope) { e.Nonce = "AA" },
		func(e *Envelope) { e.Ciphertext = base64.RawURLEncoding.EncodeToString(make([]byte, 4097)) },
		func(e *Envelope) { e.ProofPublicKey = "AA" }, func(e *Envelope) { e.Kind = Recovery },
	} {
		copy := envelope
		mutate(&copy)
		if err := copy.Validate(); err == nil {
			t.Fatal("accepted invalid key metadata")
		}
	}
}

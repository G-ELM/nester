package vaultsnapshot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// SignatureAlgo identifies the signing scheme in Envelope, so a future
// change (e.g. moving to an asymmetric signature verifiable without sharing
// the signing secret) can add a new value without breaking older envelopes
// already sitting in cold storage.
const SignatureAlgoHMACSHA256 = "hmac-sha256"

// Envelope is what actually gets written to cold storage: the snapshot plus
// enough to verify it wasn't altered afterward, without needing the
// primary database — only the signing key, which is deliberately not
// stored alongside it.
type Envelope struct {
	Snapshot  Snapshot `json:"snapshot"`
	Algo      string   `json:"algo"`
	Signature string   `json:"signature"`
}

// Sign builds an Envelope: HMAC-SHA256 (same primitive this codebase
// already uses for webhook delivery signing, see
// service.SignWebhookPayload) over the snapshot's canonical JSON encoding,
// keyed by secret.
func Sign(secret []byte, snapshot Snapshot) (Envelope, error) {
	payload, err := snapshot.canonicalJSON()
	if err != nil {
		return Envelope{}, fmt.Errorf("vaultsnapshot: encode snapshot: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)

	return Envelope{
		Snapshot:  snapshot,
		Algo:      SignatureAlgoHMACSHA256,
		Signature: hex.EncodeToString(mac.Sum(nil)),
	}, nil
}

// Verify recomputes the signature over env.Snapshot and compares it to
// env.Signature in constant time. Returns false (never an error) on an
// algorithm it doesn't recognize, so a future algo value fails verification
// safely instead of panicking a forensic-recovery tool built against an
// older version of this package.
func Verify(secret []byte, env Envelope) (bool, error) {
	if env.Algo != SignatureAlgoHMACSHA256 {
		return false, nil
	}

	payload, err := env.Snapshot.canonicalJSON()
	if err != nil {
		return false, fmt.Errorf("vaultsnapshot: encode snapshot: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	want, err := hex.DecodeString(env.Signature)
	if err != nil {
		return false, nil
	}

	return hmac.Equal(mac.Sum(nil), want), nil
}

// MarshalEnvelope is the exact byte sequence Export writes — exported so a
// forensic-recovery tool reading a file back can re-derive the same bytes
// (e.g. to diff two copies) using this package rather than reimplementing
// JSON formatting choices.
func MarshalEnvelope(env Envelope) ([]byte, error) {
	return json.MarshalIndent(env, "", "  ")
}

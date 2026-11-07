package update

import (
	"strings"
	"testing"
)

var sampleChecksums = []byte("abc123  etapo-linux-arm64-v1.8.0.tar.gz\n")

func TestSignAndVerifyChecksums(t *testing.T) {
	k := mustGenerateKey("2026-11")
	id, err := VerifyChecksums(sampleChecksums, SignChecksums(sampleChecksums, []SigningKey{k}), []ReleaseKey{k.Public()})
	if err != nil || id != "2026-11" {
		t.Fatalf("expected the release to verify with key 2026-11, got %q, %v", id, err)
	}
}

func TestVerifyRejectsTamperedChecksums(t *testing.T) {
	k := mustGenerateKey("2026-11")
	sig := SignChecksums(sampleChecksums, []SigningKey{k})
	tampered := []byte("ffffff  etapo-linux-arm64-v1.8.0.tar.gz\n")
	if _, err := VerifyChecksums(tampered, sig, []ReleaseKey{k.Public()}); err == nil {
		t.Fatal("expected checksums changed after signing to be rejected")
	}
}

func TestVerifyRejectsForgedKeyWithSameID(t *testing.T) {
	signer := mustGenerateKey("2026-11")
	forged := mustGenerateKey("2026-11")
	sig := SignChecksums(sampleChecksums, []SigningKey{forged})
	if _, err := VerifyChecksums(sampleChecksums, sig, []ReleaseKey{signer.Public()}); err == nil {
		t.Fatal("expected a signature from another key with the same ID to be rejected")
	}
}

func TestVerifyRejectsUnknownKeyID(t *testing.T) {
	k := mustGenerateKey("unlisted")
	trusted := mustGenerateKey("2026-11")
	sig := SignChecksums(sampleChecksums, []SigningKey{k})
	if _, err := VerifyChecksums(sampleChecksums, sig, []ReleaseKey{trusted.Public()}); err == nil || !strings.Contains(err.Error(), "trusted release key") {
		t.Fatalf("expected an untrusted key error, got %v", err)
	}
}

func TestVerifyReportsUnsignedRelease(t *testing.T) {
	k := mustGenerateKey("2026-11")
	_, err := VerifyChecksums(sampleChecksums, nil, []ReleaseKey{k.Public()})
	if err == nil || !strings.Contains(err.Error(), "no signature") {
		t.Fatalf("expected a missing signature error, got %v", err)
	}
}

func TestRotationAcceptsOldOrNewKey(t *testing.T) {
	oldKey := mustGenerateKey("2026-11")
	newKey := mustGenerateKey("2027-11")
	bridge := SignChecksums(sampleChecksums, []SigningKey{oldKey, newKey})

	if id, err := VerifyChecksums(sampleChecksums, bridge, []ReleaseKey{oldKey.Public()}); err != nil || id != "2026-11" {
		t.Errorf("binary that trusts only the old key should accept the bridge release, got %q, %v", id, err)
	}
	if id, err := VerifyChecksums(sampleChecksums, bridge, []ReleaseKey{newKey.Public()}); err != nil || id != "2027-11" {
		t.Errorf("binary that trusts only the new key should accept the bridge release, got %q, %v", id, err)
	}

	onlyNew := SignChecksums(sampleChecksums, []SigningKey{newKey})
	if _, err := VerifyChecksums(sampleChecksums, onlyNew, []ReleaseKey{oldKey.Public()}); err == nil {
		t.Error("a release signed only with the new key must not be accepted by a binary that trusts only the old key")
	}
}

func TestParseSigningKeysRoundTrip(t *testing.T) {
	k := mustGenerateKey("2026-11")
	keys, err := ParseSigningKeys(FormatSigningKey(k) + "\n\n")
	if err != nil || len(keys) != 1 || keys[0].ID != "2026-11" {
		t.Fatalf("expected the private key to parse back, got %+v, %v", keys, err)
	}
	pub, err := ParseReleaseKey(FormatReleaseKey(k.Public()))
	if err != nil || pub.ID != "2026-11" || !pub.PublicKey.Equal(k.Public().PublicKey) {
		t.Fatalf("expected the public key to parse back, got %+v, %v", pub, err)
	}
}

func TestParseSigningKeysRejectsBadInput(t *testing.T) {
	for _, text := range []string{"", "2026-11", "2026-11 not-base64!", "2026-11 AAAA"} {
		if _, err := ParseSigningKeys(text); err == nil {
			t.Errorf("expected %q to be rejected", text)
		}
	}
	if _, err := ParseReleaseKey("2026-11 AAAA"); err == nil {
		t.Error("expected a short public key to be rejected")
	}
}

func TestBuiltinReleaseKeysAreValid(t *testing.T) {
	if len(builtinReleaseKeys) == 0 {
		t.Fatal("etapo has no built-in release key, so no release can be installed")
	}
	for _, k := range builtinReleaseKeys {
		if k.ID == "" || len(k.PublicKey) != 32 {
			t.Errorf("built-in release key %q is malformed", k.ID)
		}
	}
}

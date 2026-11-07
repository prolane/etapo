package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ReleaseKey is a public key that may sign release checksums.
type ReleaseKey struct {
	ID        string
	PublicKey ed25519.PublicKey
}

// SigningKey is a private key that the release pipeline uses to sign checksums.
type SigningKey struct {
	ID         string
	PrivateKey ed25519.PrivateKey
}

// GenerateSigningKey creates a new Ed25519 key pair with the given ID.
func GenerateSigningKey(id string) (SigningKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SigningKey{}, fmt.Errorf("generate key: %w", err)
	}
	return SigningKey{ID: id, PrivateKey: priv}, nil
}

// Public returns the public half of the key.
func (k SigningKey) Public() ReleaseKey {
	return ReleaseKey{ID: k.ID, PublicKey: k.PrivateKey.Public().(ed25519.PublicKey)}
}

// FormatSigningKey returns the line that stores a private key, "<id> <base64 private key>".
func FormatSigningKey(k SigningKey) string {
	return k.ID + " " + base64.StdEncoding.EncodeToString(k.PrivateKey)
}

// FormatReleaseKey returns the line that lists a public key, "<id> <base64 public key>".
func FormatReleaseKey(k ReleaseKey) string {
	return k.ID + " " + base64.StdEncoding.EncodeToString(k.PublicKey)
}

// ParseSigningKeys reads the private keys in the format written by FormatSigningKey, one per line.
// The release workflow reads them from the RELEASE_SIGNING_KEYS secret.
func ParseSigningKeys(text string) ([]SigningKey, error) {
	var keys []SigningKey
	for _, line := range nonEmptyLines(text) {
		id, encoded, ok := splitKeyLine(line)
		if !ok {
			return nil, fmt.Errorf("signing key line must be \"<id> <base64 private key>\"")
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("signing key %s is not a valid Ed25519 private key", id)
		}
		keys = append(keys, SigningKey{ID: id, PrivateKey: raw})
	}
	if len(keys) == 0 {
		return nil, errors.New("no signing keys")
	}
	return keys, nil
}

// ParseReleaseKey reads one public key line written by FormatReleaseKey.
func ParseReleaseKey(line string) (ReleaseKey, error) {
	id, encoded, ok := splitKeyLine(line)
	if !ok {
		return ReleaseKey{}, errors.New("release key must be \"<id> <base64 public key>\"")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return ReleaseKey{}, fmt.Errorf("release key %s is not a valid Ed25519 public key", id)
	}
	return ReleaseKey{ID: id, PublicKey: raw}, nil
}

// SignChecksums returns the signature file for checksums. It has one line per key,
// "<id> <base64 signature>", so a release can be signed by an old and a new key during rotation.
func SignChecksums(checksums []byte, keys []SigningKey) []byte {
	var b strings.Builder
	for _, k := range keys {
		sig := ed25519.Sign(k.PrivateKey, checksums)
		fmt.Fprintf(&b, "%s %s\n", k.ID, base64.StdEncoding.EncodeToString(sig))
	}
	return []byte(b.String())
}

// VerifyChecksums checks a signature file against the release keys and returns the ID of
// the key that signed the checksums. It succeeds when any listed signature verifies with a
// key from keys. A nil keys slice means the keys built into etapo.
func VerifyChecksums(checksums, signatures []byte, keys []ReleaseKey) (string, error) {
	if keys == nil {
		keys = builtinReleaseKeys
	}

	signed := false
	sc := bufio.NewScanner(bytes.NewReader(signatures))
	for sc.Scan() {
		id, encoded, ok := splitKeyLine(sc.Text())
		if !ok {
			continue
		}
		signed = true
		sig, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		for _, k := range keys {
			if k.ID == id && ed25519.Verify(k.PublicKey, checksums, sig) {
				return id, nil
			}
		}
	}
	if !signed {
		return "", errors.New("the release has no signature")
	}
	return "", errors.New("the checksums are not signed by a trusted release key")
}

func splitKeyLine(line string) (id, encoded string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}

func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

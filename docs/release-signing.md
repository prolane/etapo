# Release signing and key rotation

Each release's `checksums.txt` is signed with Ed25519 before it is published. `etapo --update` installs a release only when the signature verifies with one of the keys built into etapo. This page explains how the keys are used, how to create them, and how to rotate them.

## How a release is signed

1. The release workflow builds the archives and writes `checksums.txt` with the SHA-256 of each archive.
2. `go run ./tools/sign-release sign dist/packages/checksums.txt` signs the file with every key in the `RELEASE_SIGNING_KEYS` secret. It writes `checksums.txt.sig`, with one line per key: `<key id> <base64 signature>`.
3. `go run ./tools/sign-release verify ...` checks that signature against the public keys in `internal/update/keys.go`. The release fails if this check fails, so a wrong secret cannot publish a release that no binary will install.
4. `etapo --update` checks the signature first, then the archive's checksum, then that the new binary reports the expected version.

Releases before the first signed release (v1.8.0) have no signature, so `--update-to` cannot install them.

## Keys

- **Key ID:** a short label, for example `2026-11` (year and month of creation). It must be unique.
- **Private key:** one line `<id> <base64 private key>` in the `RELEASE_SIGNING_KEYS` secret. The secret can hold several lines, one per active key. GitHub never shows a secret again, so keep a copy in a password manager.
- **Public key:** one line `<id> <base64 public key>` in `builtinReleaseKeys` in `internal/update/keys.go`.

To create a key:

```bash
go run ./tools/sign-release keygen 2027-11
```

The command prints both lines. Put the private line in the secret and the public line in `keys.go`. Do not commit the private line.

Current keys:

| ID | Created | In `builtinReleaseKeys` | In `RELEASE_SIGNING_KEYS` |
|---|---|---|---|
| `2026-11` | 2026-11-07 | yes | yes |

## Rotating a key

Rotation is done over three releases, so that every binary that can still be updated keeps accepting the next release. Set the secret before the merge that triggers the release, because a release is signed with the secret as it is at that moment.

1. **Create the new key** with `keygen`.
2. **Bridge release.** Add the new public key to `builtinReleaseKeys` and keep the old one. Set `RELEASE_SIGNING_KEYS` to both the old and the new line, so this release carries both signatures. Binaries that trust the old key accept it, and so do binaries that already trust the new one.
3. **Switch release.** Remove the old line from `RELEASE_SIGNING_KEYS`. The next release is signed only with the new key. It installs on binaries built from the bridge release or later. A binary built before the bridge release trusts only the old key, so it must be updated to the bridge release by hand first.
4. **Cleanup release.** Remove the old public key from `builtinReleaseKeys`. The release is signed with the new key only, and binaries that still trust the old key accept it.

The tests `TestRotationAcceptsOldOrNewKey` and `TestRunAcceptsReleaseSignedWithRotatedKey` check that a release with both signatures verifies with either key, and that a release signed only with the new key is rejected by a binary that trusts only the old key.

## If a private key may be compromised

A stolen private key lets someone sign a release that every binary still trusting that key will install. The updater cannot stop that alone, so:

1. Remove the compromised line from `RELEASE_SIGNING_KEYS` and add a new key, as in the rotation steps above.
2. Publish a release signed only with the new key, and remove the compromised public key from `builtinReleaseKeys`.
3. Tell the Pi owner (and any other installation) to install that release by hand, because a binary that still trusts the compromised key would accept an attacker's release. Verify the release by hand first, as below.

## Checking a release by hand

To verify a release without installing it, download `checksums.txt`, `checksums.txt.sig` and the archive for your platform. In a checkout of the matching tag, run:

```bash
go run ./tools/sign-release verify checksums.txt   # needs checksums.txt.sig next to it
sha256sum -c --ignore-missing checksums.txt
```

The first command checks the signature with the keys in that checkout, so the checkout must come from a trusted source.

package update

// builtinReleaseKeys are the public keys that may sign a release. A release is installed
// only when its checksums.txt carries a signature from one of them. To add or remove a key,
// follow docs/release-signing.md.
var builtinReleaseKeys = []ReleaseKey{
	mustReleaseKey("2026-11 KSsnNX4N8LXg7p2E1SFdy6r4tIp7zZwMLqLcYRbP3JQ="),
}

func mustReleaseKey(line string) ReleaseKey {
	k, err := ParseReleaseKey(line)
	if err != nil {
		panic(err)
	}
	return k
}

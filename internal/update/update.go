// Package update replaces the running etapo binary with a published GitHub release.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	Repo          = "prolane/etapo"
	DefaultAPIURL = "https://api.github.com"

	maxArchiveSize  = 100 << 20
	maxChecksumSize = 64 << 10
)

var tagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// Options controls an update.
type Options struct {
	Version    string       // release to install, for example v1.5.0; empty for the latest release
	Current    string       // version of the running binary
	Executable string       // path of the binary to replace
	APIURL     string       // GitHub API base URL; empty for DefaultAPIURL
	Client     *http.Client // nil for a client with a timeout

	// ReleaseKeys are the keys that may sign the release. nil means the keys built into etapo.
	ReleaseKeys []ReleaseKey
}

// Result describes the outcome of Run.
type Result struct {
	Version  string // release tag that is installed, or was already installed
	UpToDate bool   // true when the running version already matches; nothing was changed
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Normalize returns a version without the leading "v", so "v1.5.0" and "1.5.0" compare equal.
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// Run downloads the release build for this platform and replaces opts.Executable with it.
// The previous binary is kept next to it as <Executable>.prev.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Version != "" && !tagPattern.MatchString(opts.Version) {
		return Result{}, fmt.Errorf("invalid version %q: use the form v1.2.3", opts.Version)
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	apiURL := opts.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}

	rel, err := fetchRelease(ctx, client, apiURL, opts.Version)
	if err != nil {
		return Result{}, err
	}
	if Normalize(rel.TagName) == Normalize(opts.Current) {
		return Result{Version: rel.TagName, UpToDate: true}, nil
	}

	archiveName := fmt.Sprintf("etapo-%s-%s.tar.gz", assetPlatform(), rel.TagName)
	archiveURL, ok := findAsset(rel, archiveName)
	if !ok {
		return Result{}, fmt.Errorf("release %s has no build for this platform (%s)", rel.TagName, assetPlatform())
	}
	sumsURL, ok := findAsset(rel, "checksums.txt")
	if !ok {
		return Result{}, fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	sigURL, ok := findAsset(rel, "checksums.txt.sig")
	if !ok {
		return Result{}, fmt.Errorf("release %s is not signed (no checksums.txt.sig)", rel.TagName)
	}

	sums, err := download(ctx, client, sumsURL, maxChecksumSize)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums: %w", err)
	}
	sigs, err := download(ctx, client, sigURL, maxChecksumSize)
	if err != nil {
		return Result{}, fmt.Errorf("download checksum signature: %w", err)
	}
	keys := opts.ReleaseKeys
	if keys == nil {
		keys = builtinReleaseKeys
	}
	if _, err := VerifyChecksums(sums, sigs, keys); err != nil {
		return Result{}, fmt.Errorf("release %s: %w", rel.TagName, err)
	}

	want, err := findChecksum(sums, archiveName)
	if err != nil {
		return Result{}, err
	}

	archive, err := download(ctx, client, archiveURL, maxArchiveSize)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", archiveName, err)
	}
	digest := sha256.Sum256(archive)
	if got := hex.EncodeToString(digest[:]); got != want {
		return Result{}, fmt.Errorf("checksum mismatch for %s: expected %s, got %s", archiveName, want, got)
	}

	binary, err := extractBinary(archive)
	if err != nil {
		return Result{}, err
	}

	if err := replaceExecutable(opts.Executable, binary, rel.TagName); err != nil {
		return Result{}, err
	}
	return Result{Version: rel.TagName}, nil
}

// assetPlatform returns the platform part of a release asset name, such as
// linux-arm64 or linux-armv7 (the only 32-bit build published).
func assetPlatform() string {
	if runtime.GOARCH == "arm" {
		return runtime.GOOS + "-armv7"
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

func fetchRelease(ctx context.Context, client *http.Client, apiURL, version string) (release, error) {
	endpoint := apiURL + "/repos/" + Repo + "/releases/latest"
	if version != "" {
		endpoint = apiURL + "/repos/" + Repo + "/releases/tags/v" + Normalize(version)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("look up release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound && version != "" {
		return release{}, fmt.Errorf("no release %s in %s", version, Repo)
	}
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("look up release: GitHub answered %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxChecksumSize)).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("read release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, errors.New("release has no tag name")
	}
	return rel, nil
}

func findAsset(rel release, name string) (string, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

// findChecksum reads a checksums.txt in the format written by sha256sum.
func findChecksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// extractBinary returns the etapo program from a release archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != "etapo" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxArchiveSize))
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		return data, nil
	}
	return nil, errors.New("archive does not contain the etapo binary")
}

// replaceExecutable writes the new binary next to exe, checks that it reports the
// expected version, and then swaps it in. The old binary is kept as exe + ".prev".
func replaceExecutable(exe string, binary []byte, tag string) error {
	dir := filepath.Dir(exe)
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".etapo-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w (run with sudo, or install etapo in a directory you can write to)", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}

	out, err := exec.Command(tmpPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary does not run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), tag) {
		return fmt.Errorf("new binary reports %q, expected %s", strings.TrimSpace(string(out)), tag)
	}

	prev := exe + ".prev"
	if err := os.Rename(exe, prev); err != nil {
		return fmt.Errorf("back up current binary: %w", err)
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		_ = os.Rename(prev, exe)
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
)

// The fixture is the SLSA provenance of gh v2.103.1 linux/arm64 from cli/cli, signed by
// cli/cli's deployment workflow.
const (
	fixtureDigest     = "25b1287471a73a3cca32e5d2d2798ae26abea0243db1b9e4a8c1be506fad1bfe"
	fixtureRepo       = "cli/cli"
	fixtureWorkflow   = "cli/cli/.github/workflows/deployment.yml"
	fixtureArchiveURL = "https://github.com/cli/cli/releases/download/v2.103.1/gh_2.103.1_linux_arm64.tar.gz"
)

func fixtureBundle(t *testing.T) string {
	t.Helper()
	return readFile(t, "testdata/cli-deployment-attestation.json")
}

func fixtureDigestBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(fixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serveAttestations answers attestation lookups for the fixture digest with the JSON that
// list returns. bundleURL in that JSON points at the Snappy-compressed fixture bundle.
func serveAttestations(t *testing.T, list func(bundleURL string) string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/bundle", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(snappy.Encode(nil, []byte(fixtureBundle(t))))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/attestations/sha256:"+fixtureDigest) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, list(srv.URL+"/bundle"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func inlineBundleList(t *testing.T) func(string) string {
	return func(string) string {
		return fmt.Sprintf(`{"attestations":[{"bundle":%s}]}`, fixtureBundle(t))
	}
}

func bundleURLList(bundleURL string) string {
	return fmt.Sprintf(`{"attestations":[{"bundle_url":%q}]}`, bundleURL)
}

// fakeGH writes a stand-in for the GitHub CLI. It exits 0 when the bundle it is given
// contains successText, and otherwise prints message and exits 1. Each call is logged.
func fakeGH(t *testing.T, successText, message string) (gh, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	gh = filepath.Join(dir, "gh")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + log + "'\n" +
		"bundle=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--bundle\" ]; then bundle=\"$2\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"if grep -q '" + successText + "' \"$bundle\"; then exit 0; fi\n" +
		"echo '" + message + "'\n" +
		"exit 1\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return gh, log
}

// bundlesPassed returns the bundle files that gh was called with.
func bundlesPassed(t *testing.T, log string) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, log)), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--bundle" && i+1 < len(fields) {
				paths = append(paths, fields[i+1])
			}
		}
	}
	return paths
}

func archiveFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyAttestationPassesInlineBundleToGH(t *testing.T) {
	srv := serveAttestations(t, inlineBundleList(t))
	gh, log := fakeGH(t, "mediaType", "")

	err := verifyAttestation(context.Background(), srv.Client(), srv.URL, gh, fixtureRepo, fixtureWorkflow, fixtureDigestBytes(t), archiveFile(t))
	if err != nil {
		t.Fatalf("expected the attestation to pass, got %v", err)
	}
	calls := readFile(t, log)
	for _, want := range []string{"attestation verify", "--repo cli/cli", "--signer-workflow cli/cli/.github/workflows/deployment.yml"} {
		if !strings.Contains(calls, want) {
			t.Errorf("expected gh to be called with %q, got %q", want, calls)
		}
	}
	paths := bundlesPassed(t, log)
	if len(paths) != 1 {
		t.Fatalf("expected one bundle, got %v", paths)
	}
	if got := strings.TrimSpace(readFile(t, paths[0])); got != strings.TrimSpace(fixtureBundle(t)) {
		t.Errorf("expected the inline bundle to be passed unchanged")
	}
}

func TestVerifyAttestationDecompressesBundleURL(t *testing.T) {
	srv := serveAttestations(t, func(bundleURL string) string { return bundleURLList(bundleURL) })
	gh, log := fakeGH(t, "mediaType", "")

	err := verifyAttestation(context.Background(), srv.Client(), srv.URL, gh, fixtureRepo, fixtureWorkflow, fixtureDigestBytes(t), archiveFile(t))
	if err != nil {
		t.Fatalf("expected the attestation from the bundle URL to pass, got %v", err)
	}
	paths := bundlesPassed(t, log)
	if len(paths) != 1 {
		t.Fatalf("expected one bundle, got %v", paths)
	}
	if got := strings.TrimSpace(readFile(t, paths[0])); got != strings.TrimSpace(fixtureBundle(t)) {
		t.Errorf("expected the decompressed bundle to match the fixture")
	}
}

func TestVerifyAttestationTriesEachBundle(t *testing.T) {
	srv := serveAttestations(t, func(string) string {
		return fmt.Sprintf(`{"attestations":[{"bundle":{"not":"a bundle"}},{"bundle":%s}]}`, fixtureBundle(t))
	})
	gh, log := fakeGH(t, "mediaType", "")

	err := verifyAttestation(context.Background(), srv.Client(), srv.URL, gh, fixtureRepo, fixtureWorkflow, fixtureDigestBytes(t), archiveFile(t))
	if err != nil {
		t.Fatalf("expected the second attestation to pass, got %v", err)
	}
	if n := len(bundlesPassed(t, log)); n != 2 {
		t.Errorf("expected gh to check both bundles, got %d calls", n)
	}
}

func TestVerifyAttestationReportsRejectedBundle(t *testing.T) {
	srv := serveAttestations(t, inlineBundleList(t))
	gh, _ := fakeGH(t, "text that is not in the bundle", "Error: verifying with issuer \"sigstore.dev\"")

	err := verifyAttestation(context.Background(), srv.Client(), srv.URL, gh, fixtureRepo, "cli/cli/.github/workflows/release.yml", fixtureDigestBytes(t), archiveFile(t))
	if err == nil || !strings.Contains(err.Error(), "no attestation was signed by cli/cli/.github/workflows/release.yml") || !strings.Contains(err.Error(), "verifying with issuer") {
		t.Fatalf("expected the rejection to be reported, got %v", err)
	}
}

func TestVerifyAttestationReportsMissingAttestation(t *testing.T) {
	srv := serveAttestations(t, inlineBundleList(t))
	gh, log := fakeGH(t, "mediaType", "")
	other := sha256.Sum256([]byte("not the release archive"))

	err := verifyAttestation(context.Background(), srv.Client(), srv.URL, gh, fixtureRepo, fixtureWorkflow, other[:], archiveFile(t))
	if !errors.Is(err, errNoAttestation) {
		t.Fatalf("expected errNoAttestation, got %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("expected gh not to run without a bundle")
	}
}

func TestCheckReleaseAttestationNeedsGH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := checkReleaseAttestation(context.Background(), http.DefaultClient, "http://127.0.0.1:1", []byte("archive"))
	if err == nil || !strings.Contains(err.Error(), "GitHub CLI (gh) is needed") {
		t.Fatalf("expected a missing gh error, got %v", err)
	}
}

// TestLiveGitHubCLIVerifiesRelease runs the real GitHub CLI against the live API and a
// real release. It needs network access and is skipped unless ETAPO_LIVE_TESTS is set.
func TestLiveGitHubCLIVerifiesRelease(t *testing.T) {
	if os.Getenv("ETAPO_LIVE_TESTS") == "" {
		t.Skip("set ETAPO_LIVE_TESTS=1 to run live tests")
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh is not installed")
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(fixtureArchiveURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download %s: %s", fixtureArchiveURL, resp.Status)
	}
	archive, err := os.Create(filepath.Join(t.TempDir(), "gh.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, h), resp.Body); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != fixtureDigest {
		t.Fatalf("archive digest %s does not match the fixture", got)
	}

	if err := verifyAttestation(context.Background(), client, DefaultAPIURL, gh, fixtureRepo, fixtureWorkflow, fixtureDigestBytes(t), archive.Name()); err != nil {
		t.Fatalf("live verification failed: %v", err)
	}
	if err := verifyAttestation(context.Background(), client, DefaultAPIURL, gh, fixtureRepo, "cli/cli/.github/workflows/release.yml", fixtureDigestBytes(t), archive.Name()); err == nil {
		t.Fatal("expected the wrong workflow to be rejected")
	}
}

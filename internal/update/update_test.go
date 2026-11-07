package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRelease struct {
	tag         string
	binary      string // program that the archive's etapo entry contains
	noArchive   bool   // publish no archive for this platform
	sums        string // overrides the checksums.txt content when set
	noChecksums bool
}

func fakeBinary(version string) string {
	return fmt.Sprintf("#!/bin/sh\necho \"etapo %s\"\n", version)
}

func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveReleases runs a fake GitHub API. The first release is the latest one.
func serveReleases(t *testing.T, releases ...fakeRelease) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()

	for i, r := range releases {
		archiveName := fmt.Sprintf("etapo-%s-%s.tar.gz", assetPlatform(), r.tag)
		archive := makeArchive(t, map[string]string{"./etapo": r.binary, "./config.example.yaml": "x: 1\n"})
		sums := r.sums
		if sums == "" {
			digest := sha256.Sum256(archive)
			sums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
		}

		writeRelease := func(w http.ResponseWriter, _ *http.Request) {
			assets := []map[string]string{}
			if !r.noArchive {
				assets = append(assets, map[string]string{"name": archiveName, "browser_download_url": srv.URL + "/assets/" + r.tag + "/archive"})
			}
			if !r.noChecksums {
				assets = append(assets, map[string]string{"name": "checksums.txt", "browser_download_url": srv.URL + "/assets/" + r.tag + "/checksums"})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": r.tag, "assets": assets})
		}
		mux.HandleFunc("/repos/prolane/etapo/releases/tags/"+r.tag, writeRelease)
		if i == 0 {
			mux.HandleFunc("/repos/prolane/etapo/releases/latest", writeRelease)
		}
		mux.HandleFunc("/assets/"+r.tag+"/archive", func(w http.ResponseWriter, _ *http.Request) {
			w.Write(archive)
		})
		mux.HandleFunc("/assets/"+r.tag+"/checksums", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, sums)
		})
	}

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeExecutable(t *testing.T, content string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "etapo")
	if err := os.WriteFile(exe, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func acceptAttestation(context.Context, []byte) error { return nil }

func rejectAttestation(context.Context, []byte) error {
	return errors.New("signature does not match")
}

func TestRunInstallsLatestRelease(t *testing.T) {
	srv := serveReleases(t,
		fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")},
		fakeRelease{tag: "v1.6.0", binary: fakeBinary("v1.6.0")},
	)
	exe := writeExecutable(t, "old binary")

	res, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.UpToDate || res.Version != "v1.7.0" {
		t.Errorf("expected v1.7.0 installed, got %+v", res)
	}
	if got := readFile(t, exe); got != fakeBinary("v1.7.0") {
		t.Errorf("binary not replaced, got %q", got)
	}
	if got := readFile(t, exe+".prev"); got != "old binary" {
		t.Errorf("previous binary not kept as .prev, got %q", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected mode 0755, got %v", info.Mode().Perm())
	}
}

func TestRunInstallsRequestedVersion(t *testing.T) {
	srv := serveReleases(t,
		fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")},
		fakeRelease{tag: "v1.5.0", binary: fakeBinary("v1.5.0")},
	)
	exe := writeExecutable(t, "old binary")

	res, err := Run(context.Background(), Options{Version: "1.5.0", Current: "v1.7.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Version != "v1.5.0" {
		t.Errorf("expected v1.5.0, got %+v", res)
	}
	if got := readFile(t, exe); got != fakeBinary("v1.5.0") {
		t.Errorf("expected the requested version to be installed, got %q", got)
	}
}

func TestRunDoesNothingWhenUpToDate(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")})
	exe := writeExecutable(t, "current binary")

	res, err := Run(context.Background(), Options{Current: "1.7.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: rejectAttestation})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !res.UpToDate {
		t.Errorf("expected UpToDate, got %+v", res)
	}
	if got := readFile(t, exe); got != "current binary" {
		t.Errorf("binary changed although up to date: %q", got)
	}
	if _, err := os.Stat(exe + ".prev"); !os.IsNotExist(err) {
		t.Errorf("expected no .prev backup, got %v", err)
	}
}

func TestRunRejectsChecksumMismatch(t *testing.T) {
	srv := serveReleases(t, fakeRelease{
		tag:    "v1.7.0",
		binary: fakeBinary("v1.7.0"),
		sums:   "0000000000000000000000000000000000000000000000000000000000000000  etapo-" + assetPlatform() + "-v1.7.0.tar.gz\n",
	})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Errorf("binary changed after a failed check: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected no leftover files, found %d entries", len(entries))
	}
}

func TestRunRejectsBinaryWithWrongVersion(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v9.9.9")})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err == nil || !strings.Contains(err.Error(), "expected v1.7.0") {
		t.Fatalf("expected a version mismatch error, got %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Errorf("binary changed after a failed check: %q", got)
	}
}

func TestRunChecksAttestationOfArchive(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")})
	exe := writeExecutable(t, "old binary")

	var checked []byte
	check := func(_ context.Context, archive []byte) error {
		checked = archive
		return nil
	}
	if _, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: check}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if _, err := extractBinary(checked); err != nil {
		t.Errorf("expected the downloaded archive to be checked, got %v", err)
	}
}

func TestRunRejectsFailedAttestation(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: rejectAttestation})
	if err == nil || !strings.Contains(err.Error(), "attestation check failed") {
		t.Fatalf("expected an attestation error, got %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Errorf("binary changed after a failed attestation check: %q", got)
	}
	if _, err := os.Stat(exe + ".prev"); !os.IsNotExist(err) {
		t.Errorf("expected no .prev backup, got %v", err)
	}
}

func TestRunReportsReleaseWithoutAttestation(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")})
	exe := writeExecutable(t, "old binary")

	noAttestation := func(context.Context, []byte) error { return errNoAttestation }
	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: noAttestation})
	if err == nil || !strings.Contains(err.Error(), "has no attestation") {
		t.Fatalf("expected a missing attestation error, got %v", err)
	}
}

func TestRunReportsMissingPlatformBuild(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0"), noArchive: true})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err == nil || !strings.Contains(err.Error(), "no build for this platform") {
		t.Fatalf("expected a missing build error, got %v", err)
	}
}

func TestRunReportsMissingChecksums(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0"), noChecksums: true})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Current: "v1.6.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err == nil || !strings.Contains(err.Error(), "no checksums.txt") {
		t.Fatalf("expected a missing checksums error, got %v", err)
	}
}

func TestRunReportsUnknownVersion(t *testing.T) {
	srv := serveReleases(t, fakeRelease{tag: "v1.7.0", binary: fakeBinary("v1.7.0")})
	exe := writeExecutable(t, "old binary")

	_, err := Run(context.Background(), Options{Version: "v1.2.3", Current: "v1.7.0", Executable: exe, APIURL: srv.URL, Client: srv.Client(), VerifyAttestation: acceptAttestation})
	if err == nil || !strings.Contains(err.Error(), "no release v1.2.3") {
		t.Fatalf("expected an unknown release error, got %v", err)
	}
}

func TestRunRejectsInvalidVersion(t *testing.T) {
	exe := writeExecutable(t, "old binary")
	for _, v := range []string{"latest", "../v1.7.0", "v1.7"} {
		_, err := Run(context.Background(), Options{Version: v, Current: "v1.6.0", Executable: exe, APIURL: "http://127.0.0.1:1"})
		if err == nil || !strings.Contains(err.Error(), "invalid version") {
			t.Errorf("version %q: expected an invalid version error, got %v", v, err)
		}
	}
}

func TestExtractBinaryRequiresEtapoEntry(t *testing.T) {
	archive := makeArchive(t, map[string]string{"./config.example.yaml": "x: 1\n"})
	if _, err := extractBinary(archive); err == nil {
		t.Fatal("expected an error for an archive without the etapo binary")
	}
}

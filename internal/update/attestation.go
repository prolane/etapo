package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/golang/snappy"
)

const (
	signerWorkflow    = Repo + "/.github/workflows/release.yml"
	slsaPredicateType = "https://slsa.dev/provenance/v1"

	maxBundleSize = 1 << 20
)

// errNoAttestation means GitHub has no attestation for the archive.
var errNoAttestation = errors.New("no attestation found")

// checkReleaseAttestation verifies the archive against the attestations that the release
// workflow of this repository created for it. The GitHub CLI does the verification; it
// needs no token when it is given the attestation bundle.
func checkReleaseAttestation(ctx context.Context, client *http.Client, apiURL string, archive []byte) error {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return errors.New("the GitHub CLI (gh) is needed to verify the download: install it from https://cli.github.com")
	}

	dir, err := os.MkdirTemp("", "etapo-update-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)

	archivePath := filepath.Join(dir, "release.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		return fmt.Errorf("write temporary archive: %w", err)
	}
	digest := sha256.Sum256(archive)
	return verifyAttestation(ctx, client, apiURL, gh, Repo, signerWorkflow, digest[:], archivePath)
}

// verifyAttestation asks GitHub for the attestations of the artifact with this digest and
// checks each bundle with gh against the archive at archivePath. It succeeds when one of
// them was signed by the given workflow of the given repository.
func verifyAttestation(ctx context.Context, client *http.Client, apiURL, gh, repo, workflow string, digest []byte, archivePath string) error {
	bundles, err := fetchBundles(ctx, client, apiURL, repo, digest)
	if err != nil {
		return err
	}
	if len(bundles) == 0 {
		return errNoAttestation
	}

	dir := filepath.Dir(archivePath)
	var lastErr error
	for i, b := range bundles {
		bundlePath := filepath.Join(dir, fmt.Sprintf("bundle-%d.json", i))
		if err := os.WriteFile(bundlePath, b, 0o600); err != nil {
			return fmt.Errorf("write temporary bundle: %w", err)
		}
		if err := runGHVerify(ctx, gh, archivePath, bundlePath, repo, workflow); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("no attestation was signed by %s: %w", workflow, lastErr)
}

func runGHVerify(ctx context.Context, gh, archivePath, bundlePath, repo, workflow string) error {
	cmd := exec.CommandContext(ctx, gh, "attestation", "verify", archivePath,
		"--bundle", bundlePath, "--repo", repo, "--signer-workflow", workflow)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type attestationList struct {
	Attestations []struct {
		BundleURL string          `json:"bundle_url"`
		Bundle    json.RawMessage `json:"bundle"`
	} `json:"attestations"`
}

// fetchBundles lists the SLSA provenance attestations of an artifact and returns each
// Sigstore bundle as JSON. GitHub returns a bundle either inline or as a Snappy-compressed
// file at a URL. The API needs no token for public repositories.
func fetchBundles(ctx context.Context, client *http.Client, apiURL, repo string, digest []byte) ([][]byte, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/attestations/sha256:%s?per_page=30&predicate_type=%s",
		apiURL, repo, hex.EncodeToString(digest), url.QueryEscape(slsaPredicateType))

	body, status, err := get(ctx, client, endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("look up attestations: %w", err)
	}
	if status == http.StatusNotFound {
		return nil, errNoAttestation
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("look up attestations: GitHub answered %d", status)
	}

	var list attestationList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("read attestations: %w", err)
	}

	bundles := make([][]byte, 0, len(list.Attestations))
	for _, a := range list.Attestations {
		switch {
		case a.BundleURL != "":
			b, err := fetchBundle(ctx, client, a.BundleURL)
			if err != nil {
				return nil, err
			}
			bundles = append(bundles, b)
		case len(a.Bundle) > 0 && string(a.Bundle) != "null":
			bundles = append(bundles, a.Bundle)
		default:
			return nil, errors.New("attestation has neither a bundle nor a bundle URL")
		}
	}
	return bundles, nil
}

func fetchBundle(ctx context.Context, client *http.Client, bundleURL string) ([]byte, error) {
	body, status, err := get(ctx, client, bundleURL, "")
	if err != nil {
		return nil, fmt.Errorf("download attestation bundle: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("download attestation bundle: GitHub answered %d", status)
	}
	decoded, err := snappy.Decode(nil, body)
	if err != nil {
		return nil, fmt.Errorf("decompress attestation bundle: %w", err)
	}
	return decoded, nil
}

func get(ctx context.Context, client *http.Client, target, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBundleSize))
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleSize+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxBundleSize {
		return nil, 0, fmt.Errorf("response larger than %d bytes", maxBundleSize)
	}
	return body, resp.StatusCode, nil
}

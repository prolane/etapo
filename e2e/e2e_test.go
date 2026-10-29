//go:build e2e

// Package e2e runs the built etapo binary against mock plugs and checks its HTTP
// API, dashboard, event stream, relay control, CLI handling and persistence.
// Run with: go test -tags e2e -race ./e2e/
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	plugOffice = "office-network"
	plugTV     = "living-room-tv"
)

var etapoBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "etapo-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	etapoBinary = filepath.Join(dir, "etapo")
	build := exec.Command("go", "build", "-o", etapoBinary, "../cmd/etapo")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building etapo failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// instance is one running etapo process with its own config, port and database.
type instance struct {
	cmd    *exec.Cmd
	base   string
	output *bytes.Buffer
	done   chan error
	once   sync.Once
}

func writeConfig(t *testing.T, dir string, port int) string {
	t.Helper()
	cfg := fmt.Sprintf(`server:
  host: "127.0.0.1"
  port: %d

storage:
  db_path: %q
  flush_interval_sec: 1
  max_buffer_size: 100
  retention_days: 90

collector:
  poll_interval_sec: 1

billing:
  currency: "EUR"
  cost_per_kwh: 0.25

plugs:
  - id: %q
    name: "Office"
    model: "P115"
    category: "Home Office"
    type: "mock"
    mock_profile: "workstation"

  - id: %q
    name: "Living room TV"
    model: "P115"
    category: "Entertainment"
    type: "mock"
    mock_profile: "tv"
`, port, filepath.Join(dir, "etapo.db"), plugOffice, plugTV)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startEtapo runs the binary in "run" mode and waits until /health responds.
// The config and database live in dir so a second start can reuse them.
func startEtapo(t *testing.T, dir string) *instance {
	t.Helper()
	port := freePort(t)
	cfgPath := writeConfig(t, dir, port)
	inst := &instance{
		base:   fmt.Sprintf("http://127.0.0.1:%d", port),
		output: &bytes.Buffer{},
		done:   make(chan error, 1),
	}
	inst.cmd = exec.Command(etapoBinary, "--config", cfgPath, "run")
	inst.cmd.Stdout = inst.output
	inst.cmd.Stderr = inst.output
	if err := inst.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { inst.done <- inst.cmd.Wait() }()
	t.Cleanup(func() {
		inst.stop(t)
		if t.Failed() {
			t.Logf("etapo output:\n%s", inst.output.String())
		}
	})

	waitFor(t, 10*time.Second, "etapo to start", func() bool {
		resp, err := http.Get(inst.base + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return inst
}

// stop sends SIGTERM and checks the process exits cleanly. It is safe to call twice.
func (inst *instance) stop(t *testing.T) {
	t.Helper()
	inst.once.Do(func() {
		if err := inst.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("etapo exited before it was asked to stop: %v", err)
			return
		}
		select {
		case err := <-inst.done:
			if err != nil {
				t.Errorf("etapo did not exit cleanly after SIGTERM: %v", err)
			}
		case <-time.After(15 * time.Second):
			_ = inst.cmd.Process.Kill()
			t.Errorf("etapo did not exit within 15s of SIGTERM")
		}
	})
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type plugJSON struct {
	ID            string `json:"id"`
	IsOn          bool   `json:"is_on"`
	LatestReading *struct {
		PowerWatts float64 `json:"power_watts"`
		VoltageV   float64 `json:"voltage_v"`
	} `json:"latest_reading"`
}

func (inst *instance) getJSON(t *testing.T, path string, out any) int {
	t.Helper()
	resp, err := http.Get(inst.base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("GET %s: decoding JSON: %v", path, err)
		}
	}
	return resp.StatusCode
}

func (inst *instance) status(t *testing.T, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, inst.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// plugs waits until both mock plugs have a reading and returns them by ID.
func (inst *instance) plugs(t *testing.T) map[string]plugJSON {
	t.Helper()
	var result map[string]plugJSON
	waitFor(t, 10*time.Second, "both plugs to report a reading", func() bool {
		var list []plugJSON
		if inst.getJSON(t, "/api/plugs", &list) != http.StatusOK {
			return false
		}
		result = map[string]plugJSON{}
		for _, p := range list {
			result[p.ID] = p
		}
		return result[plugOffice].LatestReading != nil && result[plugTV].LatestReading != nil
	})
	return result
}

func TestDashboardAndHealth(t *testing.T) {
	inst := startEtapo(t, t.TempDir())

	resp, err := http.Get(inst.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Device Leaderboard") {
		t.Errorf("GET /: expected the dashboard, got %d", resp.StatusCode)
	}

	resp, err = http.Get(inst.base + "/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /favicon.svg: expected 200, got %d", resp.StatusCode)
	}

	var health map[string]string
	if inst.getJSON(t, "/health", &health) != http.StatusOK || health["app"] != "etapo" {
		t.Errorf("GET /health: unexpected body %v", health)
	}
}

func TestPollingReportsMockReadings(t *testing.T) {
	inst := startEtapo(t, t.TempDir())
	plugs := inst.plugs(t)

	for _, id := range []string{plugOffice, plugTV} {
		p := plugs[id]
		if !p.IsOn {
			t.Errorf("%s: expected the mock plug to start on", id)
		}
		if p.LatestReading.PowerWatts < 0 {
			t.Errorf("%s: negative power %v", id, p.LatestReading.PowerWatts)
		}
		if p.LatestReading.VoltageV != 230 {
			t.Errorf("%s: expected the 230 V the P115 reports, got %v", id, p.LatestReading.VoltageV)
		}
	}
}

func TestRelayControl(t *testing.T) {
	inst := startEtapo(t, t.TempDir())
	inst.plugs(t)

	if code := inst.status(t, http.MethodPost, "/api/plugs/"+plugTV+"/power", `{"on": false}`); code != http.StatusOK {
		t.Fatalf("switch off: expected 200, got %d", code)
	}
	waitFor(t, 5*time.Second, "the plug to report off", func() bool {
		return !inst.plugs(t)[plugTV].IsOn
	})

	if code := inst.status(t, http.MethodPost, "/api/plugs/"+plugTV+"/power", `{"on": true}`); code != http.StatusOK {
		t.Fatalf("switch on: expected 200, got %d", code)
	}
	waitFor(t, 5*time.Second, "the plug to report on", func() bool {
		return inst.plugs(t)[plugTV].IsOn
	})

	if code := inst.status(t, http.MethodPost, "/api/plugs/"+plugTV+"/power", `not json`); code != http.StatusBadRequest {
		t.Errorf("invalid body: expected 400, got %d", code)
	}
	if code := inst.status(t, http.MethodGet, "/api/plugs/"+plugTV+"/power", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET power: expected 405, got %d", code)
	}
}

func TestAPIInputValidation(t *testing.T) {
	inst := startEtapo(t, t.TempDir())
	inst.plugs(t)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"summary, default nights", http.MethodGet, "/api/summary", http.StatusOK},
		{"summary, one night", http.MethodGet, "/api/summary?nights=1", http.StatusOK},
		{"summary, zero nights", http.MethodGet, "/api/summary?nights=0", http.StatusBadRequest},
		{"summary, non-numeric nights", http.MethodGet, "/api/summary?nights=last", http.StatusBadRequest},
		{"breakdown, today", http.MethodGet, "/api/breakdown?range=today", http.StatusOK},
		{"breakdown, bad date", http.MethodGet, "/api/breakdown?range=today&date=2026-13-01", http.StatusBadRequest},
		{"month leaderboard, current month", http.MethodGet, "/api/leaderboard/month?month=" + time.Now().Format("2006-01"), http.StatusOK},
		{"month leaderboard, future month", http.MethodGet, "/api/leaderboard/month?month=2999-01", http.StatusBadRequest},
		{"month leaderboard, POST", http.MethodPost, "/api/leaderboard/month?month=2026-01", http.StatusMethodNotAllowed},
		{"history, 24h", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=24h", http.StatusOK},
		{"history, today", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=today", http.StatusOK},
		{"history, 7 days", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=7d", http.StatusOK},
		{"history, current month", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=month&month=" + time.Now().Format("2006-01"), http.StatusOK},
		{"history, future month", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=month&month=2999-01", http.StatusBadRequest},
		{"history, bad month", http.MethodGet, "/api/plugs/" + plugOffice + "/history?range=month&month=2026-13", http.StatusBadRequest},
		{"unknown plug", http.MethodGet, "/api/plugs/nope", http.StatusNotFound},
	}
	for _, tc := range cases {
		if got := inst.status(t, tc.method, tc.path, ""); got != tc.want {
			t.Errorf("%s: %s %s: expected %d, got %d", tc.name, tc.method, tc.path, tc.want, got)
		}
	}
}

func TestEventStreamDeliversReadings(t *testing.T) {
	inst := startEtapo(t, t.TempDir())

	req, _ := http.NewRequest(http.MethodGet, inst.base+"/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("event stream closed before a reading arrived")
			}
			if line == "event: reading" {
				return
			}
		case <-timeout:
			t.Fatal("no reading event within 10s")
		}
	}
}

func TestReadingsSurviveRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM shutdown is exercised on Unix only")
	}
	dir := t.TempDir()

	inst := startEtapo(t, dir)
	inst.plugs(t)
	waitFor(t, 10*time.Second, "history to be flushed to disk", func() bool {
		var points []map[string]any
		return inst.getJSON(t, "/api/plugs/"+plugOffice+"/history?range=1h", &points) == http.StatusOK && readingBuckets(points) >= 1
	})
	var before []map[string]any
	inst.getJSON(t, "/api/plugs/"+plugOffice+"/history?range=1h", &before)
	inst.stop(t)

	// Restart on the same database: the readings written before shutdown must still be there.
	restarted := startEtapo(t, dir)
	var after []map[string]any
	restarted.getJSON(t, "/api/plugs/"+plugOffice+"/history?range=1h", &after)
	if readingBuckets(after) < readingBuckets(before) {
		t.Errorf("history after restart has %d buckets with readings, expected at least %d", readingBuckets(after), readingBuckets(before))
	}
}

// readingBuckets counts the history buckets that hold a reading. Buckets without one are null.
func readingBuckets(points []map[string]any) int {
	n := 0
	for _, pt := range points {
		if pt["power_watts"] != nil {
			n++
		}
	}
	return n
}

func TestCLIRejectsBadInvocations(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, freePort(t))

	run := func(args ...string) (int, string) {
		cmd := exec.Command(etapoBinary, args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(out)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(out)
		}
		t.Fatal(err)
		return -1, ""
	}

	if code, out := run("--config", filepath.Join(dir, "missing.yaml"), "run"); code == 0 || !strings.Contains(out, "does not exist") {
		t.Errorf("missing config: expected failure mentioning the file, got exit %d: %s", code, out)
	}
	if code, out := run("--config", cfgPath, "bogus"); code != 2 || !strings.Contains(out, "unknown mode") {
		t.Errorf("unknown mode: expected exit 2 with usage, got exit %d: %s", code, out)
	}
	if code, out := run("--config", cfgPath, "run", "extra"); code != 2 || !strings.Contains(out, "too many arguments") {
		t.Errorf("extra argument: expected exit 2 with usage, got exit %d: %s", code, out)
	}
	if code, out := run("--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("--help: expected usage text and exit 0, got exit %d", code)
	}
}

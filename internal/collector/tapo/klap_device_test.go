package tapo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	testUsername = "user@example.com"
	testPassword = "secret"
	plugMAC      = "60-15-6F-00-00-01"
	otherPlugMAC = "60-15-6F-00-00-02"
)

// fakePlug answers the KLAP handshake and encrypted requests like a P115 and records what it was asked to do.
type fakePlug struct {
	mu            sync.Mutex
	mac           string
	on            bool
	key           []byte
	iv            []byte
	handshakes    int
	methods       []string
	addr          string // host:port of the KLAP server
	discoveryPort int    // UDP port that answers discovery probes with mac
}

func testAuthHash() []byte {
	return sha256Hash(concat(sha1Hash([]byte(testUsername)), sha1Hash([]byte(testPassword))))
}

func (p *fakePlug) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/app/handshake1":
		p.handshake1(w, body)
	case "/app/handshake2":
		p.handshake2(w, body)
	case "/app/request":
		p.request(w, r, body)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakePlug) handshake1(w http.ResponseWriter, localSeed []byte) {
	remoteSeed := make([]byte, 16)
	if _, err := rand.Read(remoteSeed); err != nil || len(localSeed) != 16 {
		http.Error(w, "bad seed", http.StatusBadRequest)
		return
	}
	authHash := testAuthHash()

	p.mu.Lock()
	p.handshakes++
	p.key = sha256Hash(concat([]byte("lsk"), localSeed, remoteSeed, authHash))[:16]
	p.iv = sha256Hash(concat([]byte("iv"), localSeed, remoteSeed, authHash))[:12]
	p.mu.Unlock()

	_, _ = w.Write(concat(remoteSeed, sha256Hash(concat(localSeed, remoteSeed, authHash))))
}

func (p *fakePlug) handshake2(w http.ResponseWriter, _ []byte) {
	w.WriteHeader(http.StatusOK)
}

func (p *fakePlug) request(w http.ResponseWriter, r *http.Request, body []byte) {
	if len(body) < 32 {
		http.Error(w, "short body", http.StatusBadRequest)
		return
	}
	seq, _ := strconv.Atoi(r.URL.Query().Get("seq"))

	p.mu.Lock()
	defer p.mu.Unlock()

	iv := concat(p.iv, int32ToBytes(int32(seq)))
	plain, err := decryptAES(body[32:], p.key, iv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Method string                 `json:"method"`
		Params map[string]interface{} `json:"params"`
	}
	if err := json.Unmarshal(plain, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.methods = append(p.methods, req.Method)

	result := map[string]interface{}{}
	switch req.Method {
	case "get_device_info":
		result = map[string]interface{}{"mac": p.mac, "device_on": p.on, "model": "P115(EU)"}
	case "set_device_info":
		if on, ok := req.Params["device_on"].(bool); ok {
			p.on = on
		}
	}

	resp, err := json.Marshal(map[string]interface{}{"error_code": 0, "result": result})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	enc, err := encryptAES(resp, p.key, iv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(concat(make([]byte, 32), enc))
}

func (p *fakePlug) calledMethod(method string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.methods {
		if m == method {
			return true
		}
	}
	return false
}

func (p *fakePlug) handshakeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handshakes
}

func (p *fakePlug) isOn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.on
}

// newFakePlug starts a KLAP server and a discovery responder that both report mac.
func newFakePlug(t *testing.T, mac string) *fakePlug {
	t.Helper()
	plug := &fakePlug{mac: mac, on: true}
	srv := httptest.NewServer(plug)
	t.Cleanup(srv.Close)
	plug.addr = strings.TrimPrefix(srv.URL, "http://")
	plug.discoveryPort = startDiscoveryResponder(t, mac)
	return plug
}

// driver returns a driver for this plug that expects expectedMAC.
func (p *fakePlug) driver(expectedMAC string) *Driver {
	d := NewDriver(p.addr, expectedMAC, testUsername, testPassword)
	d.discoveryPort = p.discoveryPort
	return d
}

// startDiscoveryResponder answers discovery probes on the loopback address with mac and returns the UDP port.
func startDiscoveryResponder(t *testing.T, mac string) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	reply := append(make([]byte, 16), []byte(`{"result":{"mac":"`+mac+`"}}`)...)
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if bytes.Equal(buf[:n], discoveryProbe) {
				_, _ = conn.WriteToUDP(reply, addr)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// silentDiscoveryPort returns a UDP port that receives probes and never answers them.
func silentDiscoveryPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestCommandsReachTheExpectedPlug(t *testing.T) {
	plug := newFakePlug(t, plugMAC)
	d := plug.driver(plugMAC)

	info, err := d.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if info.MAC != plugMAC {
		t.Errorf("expected MAC %s, got %s", plugMAC, info.MAC)
	}

	if err := d.SetPower(context.Background(), false); err != nil {
		t.Fatalf("SetPower failed: %v", err)
	}
	if plug.isOn() {
		t.Error("expected the plug to be switched off")
	}
}

func TestAnotherPlugIsRefusedBeforeLogin(t *testing.T) {
	// Another plug of the same account answers at the address that was configured
	plug := newFakePlug(t, otherPlugMAC)
	d := plug.driver(plugMAC)

	if _, err := d.GetInfo(context.Background()); !errors.Is(err, ErrWrongDevice) {
		t.Fatalf("expected ErrWrongDevice from GetInfo, got %v", err)
	}
	if err := d.SetPower(context.Background(), false); !errors.Is(err, ErrWrongDevice) {
		t.Fatalf("expected ErrWrongDevice from SetPower, got %v", err)
	}
	if plug.handshakeCount() != 0 {
		t.Errorf("expected no session with the other plug, got %d handshakes", plug.handshakeCount())
	}
	if plug.calledMethod("set_device_info") {
		t.Error("a command was sent to the other plug")
	}
}

func TestLoginChecksTheMACWhenDiscoveryIsSilent(t *testing.T) {
	plug := newFakePlug(t, otherPlugMAC)
	d := plug.driver(plugMAC)
	d.discoveryPort = silentDiscoveryPort(t)

	if _, err := d.GetInfo(context.Background()); !errors.Is(err, ErrWrongDevice) {
		t.Fatalf("expected ErrWrongDevice after the login, got %v", err)
	}
	if plug.calledMethod("set_device_info") || plug.calledMethod("get_energy_usage") {
		t.Error("the other plug was sent commands after the MAC check failed")
	}
}

func TestMACIsLearnedOnFirstLogin(t *testing.T) {
	plug := newFakePlug(t, plugMAC)
	d := plug.driver("")

	if _, err := d.GetInfo(context.Background()); err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if d.mac != plugMAC {
		t.Errorf("expected the MAC to be learned as %s, got %q", plugMAC, d.mac)
	}
}

func TestDeviceWithoutMACIsRefused(t *testing.T) {
	plug := newFakePlug(t, "")
	d := plug.driver("")

	if _, err := d.GetInfo(context.Background()); err == nil {
		t.Fatal("expected an error for a device that reports no MAC address")
	}
	if plug.calledMethod("set_device_info") {
		t.Error("a command was sent to a device without a MAC address")
	}
}

func TestRelocateReachesThePlugAtItsNewAddress(t *testing.T) {
	oldPlug := newFakePlug(t, otherPlugMAC) // Another plug has taken the old address
	newPlug := newFakePlug(t, plugMAC)
	d := oldPlug.driver(plugMAC)

	if _, err := d.GetInfo(context.Background()); !errors.Is(err, ErrWrongDevice) {
		t.Fatalf("expected ErrWrongDevice at the old address, got %v", err)
	}

	d.Relocate(newPlug.addr)
	if d.IP() != newPlug.addr {
		t.Fatalf("expected IP %s after Relocate, got %s", newPlug.addr, d.IP())
	}
	d.discoveryPort = newPlug.discoveryPort
	info, err := d.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo at the new address failed: %v", err)
	}
	if info.MAC != plugMAC {
		t.Errorf("expected MAC %s at the new address, got %s", plugMAC, info.MAC)
	}
}

package tapo

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"60-15-6f-00-00-01":    "60-15-6F-00-00-01",
		"60:15:6F:00:00:01":    "60-15-6F-00-00-01",
		"  60-15-6F-00-00-01 ": "60-15-6F-00-00-01",
		"not a mac":            "",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDiscoveryReply(t *testing.T) {
	// A reply is a 16-byte header followed by JSON. The MAC address is in clear text in the result.
	reply := append(make([]byte, 16), []byte(`{"error_code":0,"result":{"device_type":"SMART.TAPOPLUG","device_model":"P115(EU)","mac":"60-15-6F-00-00-01"}}`)...)

	mac, ok := parseDiscoveryReply(reply)
	if !ok || mac != plugMAC {
		t.Fatalf("expected MAC %s, got %q (ok=%v)", plugMAC, mac, ok)
	}
	if _, ok := parseDiscoveryReply(discoveryProbe); ok {
		t.Error("the probe itself must not be read as a reply")
	}
	if _, ok := parseDiscoveryReply([]byte("not json at all, long enough to pass the header check")); ok {
		t.Error("invalid JSON must not be read as a reply")
	}
}

func TestScanFindsDevicesByMAC(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	// A fake plug that answers every probe it receives
	go func() {
		buf := make([]byte, 512)
		reply := append(make([]byte, 16), []byte(`{"result":{"mac":"60-15-6F-00-00-01"}}`)...)
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

	found, err := Scan(context.Background(), []netip.Addr{netip.MustParseAddr("127.0.0.1")}, port, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if got := found[plugMAC]; got != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("expected %s at 127.0.0.1, got %v (all: %v)", plugMAC, got, found)
	}
}

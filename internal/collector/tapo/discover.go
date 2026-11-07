package tapo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// DiscoveryPort is the UDP port Tapo devices answer discovery probes on.
const DiscoveryPort = 20002

// addressCheckWait is how long a login waits for the device at its address to answer the discovery probe.
const addressCheckWait = time.Second

// discoveryProbe is the 16-byte probe the Tapo app sends to find devices on the local network.
var discoveryProbe = []byte{0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46, 0x3c, 0xb5, 0xd3}

// NormalizeMAC returns a MAC address in the upper-case, dash-separated form the devices report, or "" if it is not one.
func NormalizeMAC(mac string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(hw.String(), ":", "-"))
}

// parseDiscoveryReply returns the normalized MAC address from a discovery reply: a 16-byte header followed by JSON.
func parseDiscoveryReply(data []byte) (string, bool) {
	if len(data) <= len(discoveryProbe) {
		return "", false
	}
	var reply struct {
		Result struct {
			MAC string `json:"mac"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data[len(discoveryProbe):], &reply); err != nil {
		return "", false
	}
	mac := NormalizeMAC(reply.Result.MAC)
	return mac, mac != ""
}

// checkAddress asks the device at the driver's address for its MAC address before a session is opened. A different
// plug there is refused without a login, so the other plug's session is left alone. If nothing answers, the login
// still checks the MAC address.
func (d *Driver) checkAddress(ctx context.Context) error {
	if d.mac == "" {
		return nil
	}
	host := d.ip
	if h, _, err := net.SplitHostPort(d.ip); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	mac, err := probe(ctx, addr, d.discoveryPort, addressCheckWait)
	if err != nil {
		return nil
	}
	if mac != "" && mac != d.mac {
		return fmt.Errorf("%w: expected %s at %s, found %s", ErrWrongDevice, d.mac, d.ip, mac)
	}
	return nil
}

// probe asks the device at host for its MAC address. It returns as soon as the device answers, or "" after wait.
func probe(ctx context.Context, host netip.Addr, port int, wait time.Duration) (string, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return "", err
	}
	defer conn.Close()

	target := &net.UDPAddr{IP: host.AsSlice(), Port: port}
	for round := 0; round < 2; round++ {
		if _, err := conn.WriteToUDP(discoveryProbe, target); err != nil {
			return "", err
		}
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", nil
		}
		if !addr.IP.Equal(target.IP) {
			continue
		}
		if mac, ok := parseDiscoveryReply(buf[:n]); ok {
			return mac, nil
		}
	}
}

// Scan sends the discovery probe to each host and returns the address that answered for each MAC address.
// Replies are collected for wait, or until ctx is done. Hosts that do not answer are simply absent from the result.
func Scan(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Two rounds, because UDP on Wi-Fi drops packets. Replies to the second round are merged by MAC address.
	for round := 0; round < 2; round++ {
		for _, host := range hosts {
			_, _ = conn.WriteToUDP(discoveryProbe, &net.UDPAddr{IP: host.AsSlice(), Port: port})
		}
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	found := make(map[string]netip.Addr)
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return found, err
			}
			return found, nil
		}
		mac, ok := parseDiscoveryReply(buf[:n])
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(addr.IP); ok {
			found[mac] = ip.Unmap()
		}
	}
}

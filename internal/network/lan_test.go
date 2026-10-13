package network

import (
	"errors"
	"net"
	"strings"
	"testing"
)

const up = net.FlagUp | net.FlagBroadcast

func ips(s ...string) []net.IP {
	var out []net.IP
	for _, x := range s {
		out = append(out, net.ParseIP(x))
	}
	return out
}

func TestSelectPrefersPhysicalHomeNetwork(t *testing.T) {
	sel, err := Select([]Interface{
		{Name: "lo", Flags: up | net.FlagLoopback, IPs: ips("127.0.0.1")},
		{Name: "docker0", Flags: up, IPs: ips("172.17.0.1")},
		{Name: "br-1234", Flags: up, IPs: ips("172.18.0.1")},
		{Name: "tailscale0", Flags: up | net.FlagPointToPoint, IPs: ips("100.97.1.2")},
		{Name: "eth0", Flags: up, IPs: ips("10.0.0.5", "fe80::1")},
		{Name: "wlan0", Flags: up, IPs: ips("192.168.1.25")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Chosen.IP.String() != "192.168.1.25" || sel.Chosen.Interface != "wlan0" {
		t.Fatalf("chose %v", sel.Chosen)
	}
	if len(sel.Others) != 1 || sel.Others[0].Interface != "eth0" {
		t.Fatalf("others %v", sel.Others)
	}
	joined := strings.Join(sel.Warnings, "\n")
	if !strings.Contains(joined, "--bind") || !strings.Contains(joined, "tailscale0") {
		t.Fatalf("warnings %q", joined)
	}
}

func TestSelectSkipsDownAndLinkLocal(t *testing.T) {
	_, err := Select([]Interface{
		{Name: "eth0", Flags: 0, IPs: ips("192.168.1.2")},
		{Name: "eth1", Flags: up, IPs: ips("169.254.10.10")},
	})
	if !errors.Is(err, ErrNoLAN) {
		t.Fatalf("got %v", err)
	}
}

func TestSelectPublicOnly(t *testing.T) {
	_, err := Select([]Interface{
		{Name: "eno1", Flags: up, IPs: ips("65.109.30.40")},
		{Name: "virbr0", Flags: up, IPs: ips("192.168.122.1")},
	})
	if !errors.Is(err, ErrNoLAN) || !strings.Contains(err.Error(), "eno1 65.109.30.40") || strings.Contains(err.Error(), "virbr0") {
		t.Fatalf("got %v", err)
	}
}

func TestSelectSingle(t *testing.T) {
	sel, err := Select([]Interface{{Name: "en0", Flags: up, IPs: ips("192.168.0.10")}})
	if err != nil || sel.Chosen.Interface != "en0" || len(sel.Warnings) != 0 {
		t.Fatalf("%+v %v", sel, err)
	}
}

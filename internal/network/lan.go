// Package network picks the LAN address to listen on.
package network

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Address is a candidate interface address.
type Address struct {
	Interface string
	IP        net.IP
}

func (a Address) String() string { return fmt.Sprintf("%s (%s)", a.IP, a.Interface) }

// Selection is the outcome of LAN detection.
type Selection struct {
	Chosen   Address
	Others   []Address
	Warnings []string
}

// ErrNoLAN means no private IPv4 address was found on a physical-looking
// interface.
var ErrNoLAN = errors.New("no private LAN address found")

// Interface is the subset of net.Interface used for selection, so tests
// can supply fixtures.
type Interface struct {
	Name  string
	Flags net.Flags
	IPs   []net.IP
}

// SelectLAN inspects the system's interfaces.
func SelectLAN() (Selection, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return Selection{}, err
	}
	var list []Interface
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		item := Interface{Name: ifc.Name, Flags: ifc.Flags}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				item.IPs = append(item.IPs, n.IP)
			}
		}
		list = append(list, item)
	}
	return Select(list)
}

// virtualPrefixes are interface names created by container runtimes,
// hypervisors and bridges. Devices on the LAN cannot reach them.
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "lxc", "lxd", "cni", "flannel",
	"podman", "cali", "weave", "kube", "vnic", "bridge", "awdl", "llw", "anpi", "ap1",
}

// vpnPrefixes are tunnels: usable, but not what "same Wi-Fi" users expect.
var vpnPrefixes = []string{"tun", "tap", "utun", "wg", "tailscale", "zt", "ppp", "ipsec", "nordlynx", "proton"}

func hasPrefix(name string, prefixes []string) bool {
	n := strings.ToLower(name)
	for _, p := range prefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Select ranks interfaces and picks one private IPv4 address.
func Select(ifs []Interface) (Selection, error) {
	var sel Selection
	var cands []Address
	var vpns []string
	var seen []string

	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		virtual := hasPrefix(ifc.Name, virtualPrefixes)
		vpn := hasPrefix(ifc.Name, vpnPrefixes) || ifc.Flags&net.FlagPointToPoint != 0
		for _, ip := range ifc.IPs {
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			if !virtual {
				seen = append(seen, fmt.Sprintf("%s %s", ifc.Name, ip4))
			}
			if cgnat.Contains(ip4) {
				vpn = true
			}
			switch {
			case virtual, ip4.IsLinkLocalUnicast():
				continue
			case vpn:
				vpns = append(vpns, ifc.Name)
			case ip4.IsPrivate():
				cands = append(cands, Address{Interface: ifc.Name, IP: ip4})
			}
		}
	}

	if len(cands) == 0 {
		if len(seen) > 0 {
			return sel, fmt.Errorf("%w (found %s)", ErrNoLAN, strings.Join(seen, ", "))
		}
		return sel, ErrNoLAN
	}

	sort.SliceStable(cands, func(i, j int) bool { return rank(cands[i]) < rank(cands[j]) })
	sel.Chosen = cands[0]
	sel.Others = cands[1:]
	if len(sel.Others) > 0 {
		var others []string
		for _, o := range sel.Others {
			others = append(others, o.String())
		}
		sel.Warnings = append(sel.Warnings, fmt.Sprintf(
			"Several networks found; using %s. Others: %s. Pick one with --bind if the link doesn't open.",
			sel.Chosen, strings.Join(others, ", ")))
	}
	if len(vpns) > 0 {
		sel.Warnings = append(sel.Warnings, fmt.Sprintf(
			"A VPN is active (%s). Devices may need to be on the same local network, not just the VPN.",
			strings.Join(dedupe(vpns), ", ")))
	}
	return sel, nil
}

// rank prefers wired/wireless names and home-router ranges.
func rank(a Address) int {
	r := 0
	n := strings.ToLower(a.Interface)
	switch {
	case strings.HasPrefix(n, "en"), strings.HasPrefix(n, "eth"), strings.HasPrefix(n, "wl"),
		strings.HasPrefix(n, "wi-fi"), strings.HasPrefix(n, "ethernet"):
	default:
		r += 10
	}
	switch {
	case a.IP[0] == 192 && a.IP[1] == 168:
	case a.IP[0] == 10:
		r += 1
	default:
		r += 2
	}
	return r
}

func dedupe(in []string) []string {
	var out []string
	m := map[string]bool{}
	for _, s := range in {
		if !m[s] {
			m[s] = true
			out = append(out, s)
		}
	}
	return out
}

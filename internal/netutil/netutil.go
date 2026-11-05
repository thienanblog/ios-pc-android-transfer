// Package netutil finds the addresses phones on the same Wi-Fi can use to
// reach this computer.
package netutil

import (
	"net"
	"sort"
	"strings"
)

// Candidate is one IPv4 address of this computer.
type Candidate struct {
	IP        string `json:"ip"`
	Interface string `json:"interface"`
	score     int
}

// virtualHints are interface name fragments of VPNs, containers and VMs,
// which phones on the Wi-Fi cannot reach.
var virtualHints = []string{
	"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "virtualbox", "vmware",
	"vethernet", "hyper-v", "wsl", "utun", "tun", "tap", "zt", "tailscale", "wg", "llw", "awdl",
}

// LANAddresses lists usable IPv4 addresses, the most likely Wi-Fi/LAN one first.
func LANAddresses() []Candidate {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var raw []Candidate
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				raw = append(raw, Candidate{IP: ip4.String(), Interface: ifc.Name})
			}
		}
	}
	return Rank(raw)
}

// Rank drops unusable addresses and sorts the rest, best first.
func Rank(in []Candidate) []Candidate {
	var out []Candidate
	for _, c := range in {
		ip := net.ParseIP(c.IP).To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		c.score = score(ip, c.Interface)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

func score(ip net.IP, iface string) int {
	s := 0
	switch {
	case ip[0] == 192 && ip[1] == 168:
		s = 300
	case ip[0] == 10:
		s = 200
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		s = 150
	case ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127: // carrier-grade NAT / VPN overlays
		s = 20
	default:
		s = 50
	}
	name := strings.ToLower(iface)
	for _, h := range virtualHints {
		if strings.Contains(name, h) {
			s -= 250
			break
		}
	}
	for _, h := range []string{"wi-fi", "wifi", "wlan", "wlp", "en0", "ethernet", "eth", "enp", "en"} {
		if strings.HasPrefix(name, h) {
			s += 20
			break
		}
	}
	return s
}

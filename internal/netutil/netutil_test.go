package netutil

import "testing"

func TestRank(t *testing.T) {
	in := []Candidate{
		{IP: "172.17.0.1", Interface: "docker0"},
		{IP: "169.254.10.2", Interface: "eth1"},
		{IP: "10.0.0.5", Interface: "eth0"},
		{IP: "192.168.1.20", Interface: "Wi-Fi"},
		{IP: "100.101.102.103", Interface: "tailscale0"},
		{IP: "192.168.56.1", Interface: "VirtualBox Host-Only Network"},
		{IP: "not-an-ip", Interface: "x"},
	}
	got := Rank(in)
	want := []string{"192.168.1.20", "10.0.0.5", "192.168.56.1", "172.17.0.1", "100.101.102.103"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, w := range want {
		if got[i].IP != w {
			t.Fatalf("position %d: got %s want %s (all: %v)", i, got[i].IP, w, got)
		}
	}
}

func TestLANAddressesRuns(t *testing.T) {
	for _, c := range LANAddresses() {
		if c.IP == "127.0.0.1" {
			t.Fatal("loopback returned")
		}
	}
}

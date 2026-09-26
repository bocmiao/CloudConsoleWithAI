package tencent

import "testing"

// A rule from a security group or an IPv6 range does not allow everyone.
func TestFirewallRuleSources(t *testing.T) {
	open := FirewallRule{Protocol: "TCP", Port: "80", Action: "ACCEPT"}
	for _, r := range []FirewallRule{
		{Protocol: "TCP", Port: "80", Action: "ACCEPT", Group: "sg-abc"},
		{Protocol: "TCP", Port: "80", Action: "ACCEPT", Ipv6: "::/0"},
		{Protocol: "TCP", Port: "80", Action: "ACCEPT", Template: "ipm-1"},
	} {
		if r.Same(open) {
			t.Errorf("%s treated as open to everyone", r.Source())
		}
	}
	if !open.Same(FirewallRule{Protocol: "tcp", Port: "80", Action: "accept", CidrBlock: "0.0.0.0/0"}) {
		t.Error("empty CIDR is everyone")
	}
}

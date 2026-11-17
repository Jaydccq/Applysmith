// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package identity

import (
	"net/netip"
	"testing"
)

func TestNonPublicHost_NamesAndLiterals(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"jobs.example.test", ""},
		{"boards.example.org.", ""},
		{"", RuleHostEmpty},
		{"localhost", RuleLocalhost},
		{"api.localhost", RuleLocalhost},
		{"printer.local", RuleLocalName},
		{"db.corp.internal", RuleLocalName},
		{"intranet", RuleSingleName},
		{"metadata.google.internal", RuleMetadata},
		{"instance-data", RuleMetadata},
		{"127.0.0.1", RuleLoopback},
		{"10.0.0.5", RulePrivate},
		{"172.16.4.1", RulePrivate},
		{"192.168.1.10", RulePrivate},
		{"169.254.169.254", RuleMetadata},
		{"169.254.10.10", RuleLinkLocal},
		{"100.64.0.1", RuleCGNAT},
		{"0.0.0.0", RuleUnspec},
		{"0.1.2.3", RuleUnspec},
		{"224.0.0.1", RuleMulticast},
		{"255.255.255.255", RulePrivate},
		{"[::1]", RuleLoopback},
		{"fd12:3456::1", RuleUniqueLoc},
		{"fe80::1", RuleLinkLocal},
		{"fd00:ec2::254", RuleMetadata},
		{"::", RuleUnspec},
		{"::ffff:127.0.0.1", RuleLoopback},
		{"::ffff:10.1.1.1", RulePrivate},
		{"::127.0.0.1", RuleLoopback},
		{"64:ff9b::a00:1", RulePrivate},
		{"2130706433", RuleLoopback},
		{"0x7f000001", RuleLoopback},
		{"0x7f.1", RuleLoopback},
		{"017700000001", RuleLoopback},
		{"0177.0.0.1", RuleLoopback},
		{"127.1", RuleLoopback},
		{"2852039166", RuleMetadata},
		{"93.184.216.34", ""},
		{"2606:2800:220:1:248:1893:25c8:1946", ""},
	} {
		if got := NonPublicHost(tc.host); got != tc.want {
			t.Errorf("NonPublicHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestNonPublicAddr_ZoneIsLinkLocal(t *testing.T) {
	ip := netip.MustParseAddr("fe80::1%en0")
	if got := NonPublicAddr(ip); got != RuleLinkLocal {
		t.Fatalf("got %q", got)
	}
}

func TestParseIPLiteral_RejectsNamesAndOverflow(t *testing.T) {
	for _, h := range []string{"example.com", "256.1.1.1", "1.2.3.4.5", "0xzz", "4294967296", "1..2", "08"} {
		if ip, ok := ParseIPLiteral(h); ok {
			t.Errorf("%q parsed as %s", h, ip)
		}
	}
}

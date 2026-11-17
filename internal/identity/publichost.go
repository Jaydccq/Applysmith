// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package identity

import (
	"net/netip"
	"strconv"
	"strings"
)

// Rules that make a destination non-public. NonPublicHost and NonPublicAddr
// return one of them, or "" for a destination that may be a public host.
const (
	RuleLocalhost  = "localhost"
	RuleLocalName  = "local-name"
	RuleSingleName = "single-label"
	RuleMetadata   = "metadata"
	RuleLoopback   = "loopback"
	RulePrivate    = "private-address"
	RuleLinkLocal  = "link-local"
	RuleCGNAT      = "carrier-grade-nat"
	RuleUniqueLoc  = "unique-local"
	RuleUnspec     = "unspecified"
	RuleMulticast  = "multicast"
	RuleHostEmpty  = "no-host"
)

var (
	cgnat    = netip.MustParsePrefix("100.64.0.0/10")
	thisNet  = netip.MustParsePrefix("0.0.0.0/8")
	reserved = netip.MustParsePrefix("240.0.0.0/4")
	nat64    = netip.MustParsePrefix("64:ff9b::/96")
	ula      = netip.MustParsePrefix("fc00::/7")
	v6Meta   = netip.MustParseAddr("fd00:ec2::254")
)

// metadataNames are the names cloud providers serve instance metadata on.
var metadataNames = map[string]bool{
	"metadata": true, "metadata.google.internal": true, "metadata.goog": true,
	"instance-data": true, "instance-data.ec2.internal": true, "metadata.azure.com": true,
}

// NonPublicHost says why a URL host cannot be a public internet host, judged
// from the name or address literal alone: localhost, a name ending in .local or
// .internal, a single-label name, a metadata name, or an address literal (also
// in decimal, hexadecimal and octal spellings and as an IPv4-mapped IPv6
// address) in a non-public range. A name that passes still has to be checked
// after it is resolved.
func NonPublicHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	switch {
	case host == "":
		return RuleHostEmpty
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return RuleLocalhost
	case metadataNames[host]:
		return RuleMetadata
	}
	if ip, ok := ParseIPLiteral(host); ok {
		return NonPublicAddr(ip)
	}
	if !strings.Contains(host, ".") {
		return RuleSingleName
	}
	for _, suffix := range []string{".local", ".internal", ".localdomain"} {
		if strings.HasSuffix(host, suffix) {
			return RuleLocalName
		}
	}
	return ""
}

// NonPublicAddr says why an address is not a public internet address, or "".
func NonPublicAddr(ip netip.Addr) string {
	if zone := ip.WithZone(""); zone != ip {
		return RuleLinkLocal // a zone only exists on a local link
	}
	ip = ip.Unmap()
	if ip.Is6() {
		if nat64.Contains(ip) {
			b := ip.As16()
			return NonPublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if b := ip.As16(); isZero(b[:12]) && !ip.IsUnspecified() && ip != netip.IPv6Loopback() {
			return NonPublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})) // IPv4-compatible
		}
	}
	switch {
	case ip == v6Meta || ip == netip.MustParseAddr("169.254.169.254"):
		return RuleMetadata
	case ip.IsLoopback():
		return RuleLoopback
	case ip.IsUnspecified() || thisNet.Contains(ip):
		return RuleUnspec
	case ula.Contains(ip):
		return RuleUniqueLoc
	case ip.IsPrivate():
		return RulePrivate
	case ip.IsLinkLocalUnicast():
		return RuleLinkLocal
	case cgnat.Contains(ip):
		return RuleCGNAT
	case ip.IsMulticast():
		return RuleMulticast
	case reserved.Contains(ip):
		return RulePrivate
	}
	return ""
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// ParseIPLiteral reads a host as an address literal, including the legacy
// IPv4 spellings a resolver library accepts: one to four numbers in decimal,
// hexadecimal (0x) or octal (leading 0), the last one filling the remaining
// bytes.
func ParseIPLiteral(host string) (netip.Addr, bool) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip, true
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := legacyNumber(p)
		if !ok {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	var v uint64
	for i, n := range nums[:len(nums)-1] {
		if n > 255 {
			return netip.Addr{}, false
		}
		v |= n << (8 * (3 - i))
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return netip.Addr{}, false
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

func legacyNumber(s string) (uint64, bool) {
	base := 10
	switch {
	case s == "":
		return 0, false
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base, s = 16, s[2:]
		if s == "" {
			return 0, true // inet_aton reads a bare 0x as zero
		}
	case len(s) > 1 && s[0] == '0':
		base, s = 8, s[1:]
	}
	n, err := strconv.ParseUint(s, base, 32)
	return n, err == nil
}

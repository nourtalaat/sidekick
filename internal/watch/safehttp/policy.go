package safehttp

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Host name limits.
const (
	maxHostLen  = 253
	maxLabelLen = 63
)

// wildcardPrefix starts a host pattern that matches every name below a
// suffix.
const wildcardPrefix = "*."

// blockedPrefixes are the private and special address ranges a request may
// not reach unless allowPrivate lists them.
var blockedPrefixes = mustPrefixes(
	// IPv4.
	"0.0.0.0/8",       // "this network", including the unspecified address
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // carrier-grade NAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, including the cloud metadata address 169.254.169.254
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.88.99.0/24",  // 6to4 relay anycast
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, including the broadcast address
	// IPv6.
	"::/128",         // unspecified
	"::1/128",        // loopback
	"::/96",          // IPv4-compatible, deprecated
	"64:ff9b:1::/48", // local-use IPv4/IPv6 translation
	"100::/64",       // discard
	"2001::/23",      // IETF protocol assignments, including Teredo
	"2001:db8::/32",  // documentation
	"2002::/16",      // 6to4
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local
	"fec0::/10",      // site-local, deprecated
	"ff00::/8",       // multicast
)

// nat64Prefix is the well-known NAT64 prefix. Its addresses carry an IPv4
// address in their last 32 bits, which is what the request reaches, so that
// address is checked instead.
var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// hostList is a parsed watch.http.allowHosts.
type hostList struct {
	names    []string
	suffixes []string
	addrs    []netip.Addr
}

// allows reports whether the host of a URL matches the list. Names match
// without regard to case or a trailing dot.
func (l hostList) allows(host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return slices.Contains(l.addrs, a.WithZone(""))
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if slices.Contains(l.names, host) {
		return true
	}

	for _, suffix := range l.suffixes {
		if strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}

	return false
}

// addressPolicy decides which addresses a request may reach.
type addressPolicy struct {
	allowed []netip.Prefix
}

// blocked reports whether a request may not reach a: an address in a
// private or special range that allowPrivate does not list. An IPv4-mapped
// or NAT64 IPv6 address is judged by the IPv4 address it carries.
func (p addressPolicy) blocked(a netip.Addr) bool {
	candidates := []netip.Addr{a.WithZone("")}
	if v4, ok := embeddedIPv4(a); ok {
		candidates = append(candidates, v4)
	}

	for _, c := range candidates {
		if containsAny(p.allowed, c) {
			return false
		}
	}

	return slices.ContainsFunc(candidates, func(c netip.Addr) bool { return containsAny(blockedPrefixes, c) })
}

// CheckHostPattern checks one watch.http.allowHosts entry: a host name, an
// IP address, or "*." and a host name, which matches every name below it
// but not the name itself.
func CheckHostPattern(pattern string) error {
	name, wildcard := strings.CutPrefix(pattern, wildcardPrefix)
	if !wildcard {
		if _, err := netip.ParseAddr(pattern); err == nil {
			return nil
		}
	}

	if err := checkHostName(strings.TrimSuffix(name, ".")); err != nil {
		return fmt.Errorf("host pattern %q: %w", pattern, err)
	}

	return nil
}

// ParsePrefix parses one watch.http.allowPrivate entry: an address range in
// CIDR form, such as 10.20.0.0/16, or one IP address.
func ParsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}

	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%q is not an address range such as 10.20.0.0/16 or an IP address", s)
	}

	return netip.PrefixFrom(a, a.BitLen()), nil
}

// parseHosts parses watch.http.allowHosts.
func parseHosts(patterns []string) (hostList, error) {
	var l hostList

	for i, pattern := range patterns {
		if err := CheckHostPattern(pattern); err != nil {
			return hostList{}, fmt.Errorf("allowHosts[%d]: %w", i, err)
		}

		if a, err := netip.ParseAddr(pattern); err == nil {
			l.addrs = append(l.addrs, a)

			continue
		}

		name := strings.ToLower(strings.TrimSuffix(pattern, "."))
		if suffix, ok := strings.CutPrefix(name, wildcardPrefix); ok {
			l.suffixes = append(l.suffixes, suffix)
		} else {
			l.names = append(l.names, name)
		}
	}

	return l, nil
}

// parsePrefixes parses watch.http.allowPrivate.
func parsePrefixes(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))

	for i, s := range entries {
		p, err := ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("allowPrivate[%d]: %w", i, err)
		}

		out = append(out, p)
	}

	return out, nil
}

// checkHostName checks a DNS host name: dot-separated labels of letters,
// digits, "-" and "_", none starting or ending with "-".
func checkHostName(name string) error {
	if name == "" {
		return errors.New("empty host name")
	}

	if len(name) > maxHostLen {
		return fmt.Errorf("longer than %d characters", maxHostLen)
	}

	for label := range strings.SplitSeq(name, ".") {
		if err := checkLabel(label); err != nil {
			return fmt.Errorf("label %q: %w", label, err)
		}
	}

	return nil
}

// checkLabel checks one label of a host name.
func checkLabel(label string) error {
	switch {
	case label == "":
		return errors.New("empty")
	case len(label) > maxLabelLen:
		return fmt.Errorf("longer than %d characters", maxLabelLen)
	case label[0] == '-' || label[len(label)-1] == '-':
		return errors.New("starts or ends with -")
	}

	for _, c := range label {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("holds %q; only letters, digits, - and _ are allowed", c)
		}
	}

	return nil
}

// embeddedIPv4 returns the IPv4 address an IPv4-mapped or NAT64 IPv6
// address carries.
func embeddedIPv4(a netip.Addr) (netip.Addr, bool) {
	if a.Is4In6() {
		return a.Unmap(), true
	}

	if a.Is6() && nat64Prefix.Contains(a.WithZone("")) {
		b := a.As16()

		return netip.AddrFrom4([4]byte(b[12:])), true
	}

	return netip.Addr{}, false
}

// containsAny reports whether one of the prefixes holds a.
func containsAny(prefixes []netip.Prefix, a netip.Addr) bool {
	return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

// mustPrefixes parses prefixes that are known to be valid.
func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}

	return out
}

package safehttp

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddressPolicyBlocked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		allowed []string
		want    bool
	}{
		// IPv4 ranges.
		{name: "this network", addr: "0.1.2.3", want: true},
		{name: "IPv4 unspecified", addr: "0.0.0.0", want: true},
		{name: "private 10/8", addr: "10.1.2.3", want: true},
		{name: "carrier-grade NAT start", addr: "100.64.0.1", want: true},
		{name: "carrier-grade NAT end", addr: "100.127.255.255", want: true},
		{name: "IPv4 loopback", addr: "127.0.0.1", want: true},
		{name: "IPv4 loopback, other address", addr: "127.10.0.5", want: true},
		{name: "IPv4 link-local", addr: "169.254.1.1", want: true},
		{name: "cloud metadata address", addr: "169.254.169.254", want: true},
		{name: "private 172.16/12 start", addr: "172.16.0.1", want: true},
		{name: "private 172.16/12 end", addr: "172.31.255.255", want: true},
		{name: "IETF protocol assignments", addr: "192.0.0.170", want: true},
		{name: "documentation 192.0.2/24", addr: "192.0.2.1", want: true},
		{name: "private 192.168/16", addr: "192.168.1.1", want: true},
		{name: "benchmarking", addr: "198.18.0.1", want: true},
		{name: "IPv4 multicast", addr: "224.0.0.251", want: true},
		{name: "IPv4 multicast end", addr: "239.255.255.255", want: true},
		{name: "reserved", addr: "240.0.0.1", want: true},
		{name: "broadcast", addr: "255.255.255.255", want: true},
		// IPv6 ranges.
		{name: "IPv6 unspecified", addr: "::", want: true},
		{name: "IPv6 loopback", addr: "::1", want: true},
		{name: "IPv4-compatible IPv6", addr: "::7f00:1", want: true},
		{name: "IPv4-mapped loopback", addr: "::ffff:127.0.0.1", want: true},
		{name: "IPv4-mapped metadata address", addr: "::ffff:169.254.169.254", want: true},
		{name: "IPv4-mapped private", addr: "::ffff:10.0.0.1", want: true},
		{name: "NAT64 of the metadata address", addr: "64:ff9b::a9fe:a9fe", want: true},
		{name: "local-use NAT64", addr: "64:ff9b:1::1", want: true},
		{name: "discard", addr: "100::1", want: true},
		{name: "Teredo", addr: "2001:0:4136:e378::1", want: true},
		{name: "IPv6 documentation", addr: "2001:db8::1", want: true},
		{name: "6to4", addr: "2002:c000:0204::1", want: true},
		{name: "unique local fc00", addr: "fc00::1", want: true},
		{name: "unique local fd00", addr: "fd12:3456::1", want: true},
		{name: "IPv6 link-local", addr: "fe80::1", want: true},
		{name: "IPv6 link-local end of range", addr: "febf:ffff::1", want: true},
		{name: "IPv6 link-local with a zone", addr: "fe80::1%eth0", want: true},
		{name: "site-local", addr: "fec0::1", want: true},
		{name: "IPv6 multicast", addr: "ff02::1", want: true},
		// Public addresses.
		{name: "public IPv4", addr: "93.184.215.14"},
		{name: "public IPv4 next to 172.16/12", addr: "172.32.0.1"},
		{name: "public IPv4 next to carrier-grade NAT", addr: "100.128.0.1"},
		{name: "public IPv4 next to link-local", addr: "169.255.0.1"},
		{name: "public IPv6", addr: "2606:4700::1111"},
		{name: "IPv4-mapped public", addr: "::ffff:93.184.215.14"},
		{name: "NAT64 of a public address", addr: "64:ff9b::5db8:d70e"},
		// allowPrivate.
		{name: "an allowed range", addr: "10.20.3.4", allowed: []string{"10.20.0.0/16"}},
		{name: "outside the allowed range", addr: "10.21.0.1", allowed: []string{"10.20.0.0/16"}, want: true},
		{name: "IPv4-mapped form of an allowed range", addr: "::ffff:10.20.3.4", allowed: []string{"10.20.0.0/16"}},
		{name: "NAT64 form of an allowed range", addr: "64:ff9b::a14:304", allowed: []string{"10.20.0.0/16"}},
		{name: "an allowed single address", addr: "fd00::1", allowed: []string{"fd00::1"}},
		{name: "next to an allowed single address", addr: "fd00::2", allowed: []string{"fd00::1"}, want: true},
		{name: "the metadata address listed on purpose", addr: "169.254.169.254", allowed: []string{"169.254.169.254"}},
		{name: "a link-local zone does not matter", addr: "fe80::1%eth0", allowed: []string{"fe80::/64"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			allowed, err := parsePrefixes(tt.allowed)
			require.NoError(t, err)

			require.Equal(t, tt.want, addressPolicy{allowed: allowed}.blocked(netip.MustParseAddr(tt.addr)))
		})
	}
}

func TestHostListAllows(t *testing.T) {
	t.Parallel()

	patterns := []string{"status.example.com", "*.internal.example.com", "Mixed.Example.org.", "10.20.0.5", "fd00::5"}

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "an exact name", host: "status.example.com", want: true},
		{name: "names ignore case", host: "STATUS.example.COM", want: true},
		{name: "a trailing dot", host: "status.example.com.", want: true},
		{name: "a pattern written in mixed case with a dot", host: "mixed.example.org", want: true},
		{name: "a name below the suffix", host: "api.internal.example.com", want: true},
		{name: "a name two levels below the suffix", host: "a.b.internal.example.com", want: true},
		{name: "the suffix itself", host: "internal.example.com"},
		{name: "a name that only ends like the suffix", host: "evilinternal.example.com"},
		{name: "a name below an exact name", host: "x.status.example.com"},
		{name: "another name", host: "example.com"},
		{name: "an allowed IPv4 address", host: "10.20.0.5", want: true},
		{name: "another IPv4 address", host: "10.20.0.6"},
		{name: "an allowed IPv6 address in another form", host: "fd00:0::5", want: true},
		{name: "a name that starts with an address", host: "10.20.0.5.internal.example.com", want: true},
		{name: "an empty host", host: ""},
	}

	l, err := parseHosts(patterns)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, l.allows(tt.host))
		})
	}
}

func TestCheckHostPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		wantErr string
	}{
		{name: "a name", pattern: "status.example.com"},
		{name: "a single label", pattern: "checkout"},
		{name: "a wildcard", pattern: "*.example.com"},
		{name: "an underscore", pattern: "my_service.internal"},
		{name: "an IPv4 address", pattern: "10.0.0.1"},
		{name: "an IPv6 address", pattern: "fd00::1"},
		{name: "empty", pattern: "", wantErr: "empty host name"},
		{name: "a bare wildcard", pattern: "*", wantErr: `label "*": holds '*'`},
		{name: "a wildcard with nothing after it", pattern: "*.", wantErr: "empty host name"},
		{name: "a wildcard inside", pattern: "api.*.example.com", wantErr: `label "*": holds '*'`},
		{name: "a port", pattern: "example.com:8080", wantErr: `holds ':'`},
		{name: "a URL", pattern: "https://example.com", wantErr: `holds ':'`},
		{name: "an empty label", pattern: "a..b", wantErr: `label "": empty`},
		{name: "a label starting with -", pattern: "-a.example.com", wantErr: `label "-a": starts or ends with -`},
		{name: "a label too long", pattern: longLabel() + ".com", wantErr: "longer than 63 characters"},
		{name: "a name too long", pattern: longName(), wantErr: "longer than 253 characters"},
		{name: "a wildcard IP address", pattern: "*.10.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckHostPattern(tt.pattern)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParsePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "an IPv4 range", in: "10.20.0.0/16", want: "10.20.0.0/16"},
		{name: "a range with host bits is masked", in: "10.20.1.2/16", want: "10.20.0.0/16"},
		{name: "an IPv4 address", in: "10.20.0.5", want: "10.20.0.5/32"},
		{name: "an IPv6 range", in: "fd00::/8", want: "fd00::/8"},
		{name: "an IPv6 address", in: "fd00::1", want: "fd00::1/128"},
		{name: "a name", in: "example.com", wantErr: true},
		{name: "an address with a zone", in: "fe80::1%eth0", wantErr: true},
		{name: "a bad length", in: "10.0.0.0/33", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePrefix(tt.in)
			if tt.wantErr {
				require.ErrorContains(t, err, "is not an address range")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got.String())
		})
	}
}

func TestNewRejectsBadLists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{name: "a bad host pattern", opts: Options{AllowHosts: []string{"ok.example.com", "bad host"}}, wantErr: "allowHosts[1]: "},
		{name: "a bad range", opts: Options{AllowPrivate: []string{"10.0.0.0/8", "nope"}}, wantErr: "allowPrivate[1]: "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(tt.opts)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// longLabel returns a label of 64 characters.
func longLabel() string {
	b := make([]byte, maxLabelLen+1)
	for i := range b {
		b[i] = 'a'
	}

	return string(b)
}

// longName returns a name of more than 253 characters.
func longName() string {
	name := "a"
	for len(name) <= maxHostLen {
		name += ".a"
	}

	return name
}

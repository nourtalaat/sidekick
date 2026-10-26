package httpclient

import (
	"net/netip"
	"strings"
)

// IsLoopbackHost reports whether host, as url.URL.Hostname returns it, is
// "localhost" in any case or a literal loopback IP address: 127.0.0.0/8,
// ::1, or an IPv4-mapped IPv6 form of 127.0.0.0/8. It does no DNS lookup,
// so a name that resolves to a loopback address is not loopback here, and
// neither is any *.localhost name. Plugins use it to allow plain http only to their
// own machine, such as a local proxy or a test server, so requests never
// cross the network unencrypted.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip, err := netip.ParseAddr(host)

	return err == nil && ip.Unmap().IsLoopback()
}

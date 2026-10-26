package httpclient

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "localhost", host: "localhost", want: true},
		{name: "localhost in mixed case", host: "LocalHost", want: true},
		{name: "localhost in capitals", host: "LOCALHOST", want: true},
		{name: "127.0.0.1", host: "127.0.0.1", want: true},
		{name: "another 127.0.0.0/8 address", host: "127.10.20.30", want: true},
		{name: "IPv6 loopback", host: "::1", want: true},
		{name: "IPv4-mapped IPv6 loopback", host: "::ffff:127.0.0.1", want: true},
		{name: "IPv4-mapped IPv6 of another 127.0.0.0/8 address", host: "::ffff:127.1.2.3", want: true},
		{name: "IPv4-mapped IPv6 of a private address", host: "::ffff:10.0.0.1"},
		{name: "a name that starts like localhost", host: "localhost.example.com"},
		{name: "a subdomain of localhost", host: "api.localhost"},
		{name: "localhost with a trailing dot", host: "localhost."},
		{name: "a name is not looked up", host: "ip6-localhost"},
		{name: "a private address", host: "10.0.3.7"},
		{name: "the unspecified address", host: "0.0.0.0"},
		{name: "IPv6 unspecified", host: "::"},
		{name: "a public name", host: "bedrock-runtime.us-east-1.amazonaws.com"},
		{name: "empty", host: ""},
		{name: "brackets are not stripped", host: "[::1]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, IsLoopbackHost(tt.host))
		})
	}
}

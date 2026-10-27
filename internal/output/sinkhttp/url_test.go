package sinkhttp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "https", raw: "https://hooks.example.com/sidekick"},
		{name: "http on localhost", raw: "http://localhost:8080/hook"},
		{name: "http on LOCALHOST", raw: "http://LOCALHOST/hook"},
		{name: "http on 127.0.0.1", raw: "http://127.0.0.1:9000/hook"},
		{name: "http on another loopback address", raw: "http://127.10.0.5/hook"},
		{name: "http on ::1", raw: "http://[::1]:9000/hook"},
		{name: "http on an IPv4-mapped loopback address", raw: "http://[::ffff:127.0.0.1]/hook"},
		{name: "http elsewhere", raw: "http://hooks.example.com/sidekick", wantErr: "url: must use https; plain http is only allowed for localhost"},
		{name: "http on a name under localhost", raw: "http://app.localhost/hook", wantErr: "url: must use https"},
		{name: "http on a private address", raw: "http://10.0.0.1/hook", wantErr: "url: must use https"},
		{name: "http on the unspecified address", raw: "http://0.0.0.0/hook", wantErr: "url: must use https"},
		{name: "a relative URL", raw: "/hook", wantErr: "url: must be an absolute https URL"},
		{name: "another scheme", raw: "ftp://hooks.example.com/", wantErr: "url: must be an absolute https URL"},
		{name: "empty", raw: "", wantErr: "url: must be an absolute https URL"},
		{name: "a user in the URL", raw: "https://sidekick@hooks.example.com/", wantErr: "url: must not hold a user name or password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckURL("url", tt.raw)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

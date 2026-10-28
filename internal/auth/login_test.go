package auth

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSealAndOpen(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	st := loginState{State: "s", Nonce: "n", Verifier: "v", Return: "/x", Expires: now.Add(loginLifetime)}

	key, err := loginKey("client-secret")
	require.NoError(t, err)

	otherKey, err := loginKey("another-secret")
	require.NoError(t, err)

	sealed, err := seal(key, st)
	require.NoError(t, err)

	tampered := []byte(sealed)
	tampered[len(tampered)-1] ^= 1

	tests := []struct {
		name    string
		key     []byte
		value   string
		at      time.Time
		wantErr bool
	}{
		{name: "the same key in time", key: key, value: sealed, at: now},
		{name: "another key", key: otherKey, value: sealed, at: now, wantErr: true},
		{name: "after it expired", key: key, value: sealed, at: now.Add(loginLifetime), wantErr: true},
		{name: "a changed value", key: key, value: string(tampered), at: now, wantErr: true},
		{name: "not base64", key: key, value: "%%%", at: now, wantErr: true},
		{name: "too short", key: key, value: "AAAA", at: now, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := open(tt.key, tt.value, tt.at)
			if tt.wantErr {
				require.ErrorIs(t, err, errBadLoginState)

				return
			}

			require.NoError(t, err)
			require.Equal(t, st.State, got.State)
			require.Equal(t, st.Nonce, got.Nonce)
			require.Equal(t, st.Verifier, got.Verifier)
			require.Equal(t, st.Return, got.Return)
			require.True(t, st.Expires.Equal(got.Expires))
		})
	}
}

func TestReturnPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{in: "", want: "/"},
		{in: "/", want: "/"},
		{in: "/investigations/1?tab=steps", want: "/investigations/1?tab=steps"},
		{in: "//evil.example.com", want: "/"},
		{in: `/\evil.example.com`, want: "/"},
		{in: "https://evil.example.com/", want: "/"},
		{in: "investigations", want: "/"},
		{in: "javascript:alert(1)", want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, returnPath(tt.in))
		})
	}
}

func TestGroupsOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "missing", raw: "", want: []string{}},
		{name: "null", raw: "null", want: []string{}},
		{name: "a list", raw: `["a","b"]`, want: []string{"a", "b"}},
		{name: "one string", raw: `"a"`, want: []string{"a"}},
		{name: "a number", raw: `7`, wantErr: true},
		{name: "a list of numbers", raw: `[1]`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := groupsOf(json.RawMessage(tt.raw))
			if tt.wantErr {
				require.ErrorIs(t, err, errBadGroups)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsFalse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "false", want: true},
		{raw: `"false"`, want: true},
		{raw: `"FALSE"`, want: true},
		{raw: "true", want: false},
		{raw: `"true"`, want: false},
		{raw: "null", want: false},
		{raw: "1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, isFalse(json.RawMessage(tt.raw)))
		})
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		want   bool
	}{
		{method: http.MethodGet, want: false},
		{method: http.MethodHead, want: false},
		{method: http.MethodOptions, want: false},
		{method: http.MethodPost, want: true},
		{method: http.MethodPut, want: true},
		{method: http.MethodPatch, want: true},
		{method: http.MethodDelete, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, changes(tt.method))
		})
	}
}

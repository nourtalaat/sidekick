package teams

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/intake"
)

// testServiceURL is the service URL of the test activities.
const testServiceURL = "https://smba.trafficmanager.net/teams/"

// testRSABits is the size of the test keys.
const testRSABits = 2048

// The paths of the fake Bot Framework login service.
const (
	metadataPath = "/v1/.well-known/openidconfiguration"
	keysPath     = "/v1/.well-known/keys"
)

// testNow is the time the verifier's clock starts at.
var testNow = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// testKey is a signing key with its ID and endorsements.
type testKey struct {
	id           string
	private      *rsa.PrivateKey
	endorsements []string
}

// keyServer is a fake Bot Framework login service whose key set a test
// can change, and which counts how often the key set is fetched.
type keyServer struct {
	server *fakehttp.Server

	mu      sync.Mutex
	keys    []testKey
	fetches int
	// failing makes every request fail with 503.
	failing bool
}

func TestVerify(t *testing.T) {
	t.Parallel()

	signer := newTestKey(t, "key-1", ChannelTeams)
	other := newTestKey(t, "key-1", ChannelTeams)
	webchat := newTestKey(t, "key-webchat", "webchat")

	tests := []struct {
		name string
		// header builds the Authorization header; nil means a valid token
		// with the claims claims makes, signed by key.
		header     func(t *testing.T) string
		claims     func(c *botClaims)
		key        testKey
		channelID  string
		serviceURL string
		want       bool
	}{
		{name: "a valid token", want: true},
		{name: "the scheme in another case", header: func(t *testing.T) string { t.Helper(); return "bearer " + sign(t, signer, validClaims()) }, want: true},
		{name: "a service URL in another case", serviceURL: "https://SMBA.trafficmanager.net/teams/", want: true},
		{name: "expired within the clock leeway", claims: func(c *botClaims) { c.ExpiresAt = jwt.NewNumericDate(testNow.Add(-4 * time.Minute)) }, want: true},
		{name: "no Authorization header", header: func(*testing.T) string { return "" }},
		{name: "another scheme", header: func(t *testing.T) string { t.Helper(); return "Basic " + sign(t, signer, validClaims()) }},
		{name: "a token that is not a JWT", header: func(*testing.T) string { return "Bearer not-a-jwt" }},
		{name: "another issuer", claims: func(c *botClaims) { c.Issuer = "https://sts.windows.net/" + testTenantID + "/" }},
		{name: "another audience", claims: func(c *botClaims) { c.Audience = jwt.ClaimStrings{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"} }},
		{name: "expired beyond the clock leeway", claims: func(c *botClaims) { c.ExpiresAt = jwt.NewNumericDate(testNow.Add(-6 * time.Minute)) }},
		{name: "no expiry", claims: func(c *botClaims) { c.ExpiresAt = nil }},
		{name: "not valid yet", claims: func(c *botClaims) { c.NotBefore = jwt.NewNumericDate(testNow.Add(10 * time.Minute)) }},
		{name: "for another service URL", claims: func(c *botClaims) { c.ServiceURL = "https://attacker.example.com/" }},
		{name: "signed by a key with a known ID but other bits", key: other},
		{name: "signed by a key not endorsed for Teams", key: webchat},
		{name: "an activity without a channel", channelID: "-"},
		{name: "an activity without a service URL", serviceURL: "-"},
		{
			name: "signed with HS256 and the public key",
			header: func(t *testing.T) string {
				t.Helper()

				token := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims())
				token.Header["kid"] = signer.id

				signed, err := token.SignedString(signer.private.N.Bytes())
				require.NoError(t, err)

				return "Bearer " + signed
			},
		},
		{
			name: "unsigned",
			header: func(t *testing.T) string {
				t.Helper()

				token := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
				token.Header["kid"] = signer.id

				signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
				require.NoError(t, err)

				return "Bearer " + signed
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys := newKeyServer(t, signer, webchat)
			v := newTestVerifier(t, keys)

			header := ""
			if tt.header != nil {
				header = tt.header(t)
			} else {
				claims := validClaims()
				if tt.claims != nil {
					tt.claims(&claims)
				}

				key := signer
				if tt.key.private != nil {
					key = tt.key
				}

				header = "Bearer " + sign(t, key, claims)
			}

			channelID, serviceURL := orDefault(tt.channelID, ChannelTeams), orDefault(tt.serviceURL, testServiceURL)

			err := v.Verify(t.Context(), header, channelID, serviceURL)
			if tt.want {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, intake.ErrUnauthenticated)
		})
	}
}

func TestVerifyKeyRefresh(t *testing.T) {
	t.Parallel()

	first := newTestKey(t, "key-1", ChannelTeams)
	second := newTestKey(t, "key-2", ChannelTeams)

	// step is one Verify call: at, after testNow, with a token signed by
	// key, before which the server's key set becomes serve (when set).
	type step struct {
		at          time.Duration
		serve       []testKey
		failing     bool
		key         testKey
		want        bool
		wantFetches int
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "the keys are fetched once and used until they are a day old",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: time.Hour, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval - time.Minute, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval, key: first, want: true, wantFetches: 2},
			},
		},
		{
			name: "a rotated key is found by fetching the keys again",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: 10 * time.Minute, serve: []testKey{first, second}, key: second, want: true, wantFetches: 2},
				{at: 11 * time.Minute, key: first, want: true, wantFetches: 2},
			},
		},
		{
			name: "a retired key stops working after the next refresh",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval, serve: []testKey{second}, key: first, wantFetches: 2},
				{at: KeysRefreshInterval + time.Minute, key: second, want: true, wantFetches: 2},
			},
		},
		{
			name: "an unknown key ID fetches the keys at most once every 5 minutes",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: 10 * time.Minute, key: second, wantFetches: 2},
				{at: 11 * time.Minute, key: second, wantFetches: 2},
				{at: 14 * time.Minute, key: second, wantFetches: 2},
				{at: 10*time.Minute + UnknownKeyRefreshInterval, key: second, wantFetches: 3},
			},
		},
		{
			name: "a key that appears within 5 minutes of a fetch waits for the next one",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: time.Minute, serve: []testKey{first, second}, key: second, wantFetches: 1},
				{at: UnknownKeyRefreshInterval, key: second, want: true, wantFetches: 2},
			},
		},
		{
			name: "a failed refresh keeps the cached keys",
			steps: []step{
				{at: 0, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval, failing: true, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval + time.Minute, key: first, want: true, wantFetches: 1},
				{at: KeysRefreshInterval + UnknownKeyRefreshInterval, key: first, want: true, wantFetches: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys := newKeyServer(t, first)
			v := newTestVerifier(t, keys)

			for i, s := range tt.steps {
				keys.set(s.serve, s.failing)

				now := testNow.Add(s.at)
				v.now = func() time.Time { return now }

				claims := validClaims()
				claims.IssuedAt = jwt.NewNumericDate(now.Add(-time.Minute))
				claims.ExpiresAt = jwt.NewNumericDate(now.Add(time.Hour))

				err := v.Verify(t.Context(), "Bearer "+sign(t, s.key, claims), ChannelTeams, testServiceURL)
				if s.want {
					require.NoError(t, err, "step %d", i)
				} else {
					require.ErrorIs(t, err, intake.ErrUnauthenticated, "step %d", i)
				}

				require.Equal(t, s.wantFetches, keys.fetchCount(), "step %d", i)
			}
		})
	}
}

func TestVerifyWithoutKeys(t *testing.T) {
	t.Parallel()

	signer := newTestKey(t, "key-1", ChannelTeams)

	tests := []struct {
		name  string
		setup func(t *testing.T, keys *keyServer)
	}{
		{name: "the login service fails", setup: func(_ *testing.T, keys *keyServer) { keys.set(nil, true) }},
		{name: "the key set is empty", setup: func(_ *testing.T, keys *keyServer) { keys.set([]testKey{}, false) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys := newKeyServer(t, signer)
			tt.setup(t, keys)

			v := newTestVerifier(t, keys)

			err := v.Verify(t.Context(), "Bearer "+sign(t, signer, validClaims()), ChannelTeams, testServiceURL)
			require.ErrorIs(t, err, errKeysUnavailable)
			require.NotErrorIs(t, err, intake.ErrUnauthenticated)
		})
	}
}

func TestNewVerifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    VerifierOptions
		wantErr bool
	}{
		{name: "the default metadata URL", opts: VerifierOptions{AppID: testAppID}},
		{name: "a loopback metadata URL", opts: VerifierOptions{AppID: testAppID, MetadataURL: "http://127.0.0.1:1234" + metadataPath}},
		{name: "no app ID", opts: VerifierOptions{}, wantErr: true},
		{name: "a metadata URL over plain http", opts: VerifierOptions{AppID: testAppID, MetadataURL: "http://login.botframework.com" + metadataPath}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewVerifier(tt.opts)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestRSAPublicKey(t *testing.T) {
	t.Parallel()

	modulus := base64.RawURLEncoding.EncodeToString([]byte{0xc5, 0x01, 0x02})

	tests := []struct {
		name     string
		exponent string
		wantE    int
		wantErr  bool
	}{
		{name: "65537", exponent: "AQAB", wantE: 65537},
		{name: "an exponent of 1", exponent: "AQ", wantErr: true},
		{name: "an exponent longer than 3 bytes", exponent: base64.RawURLEncoding.EncodeToString([]byte{1, 0, 0, 1}), wantErr: true},
		{name: "an exponent that is not base64url", exponent: "A+B/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, err := rsaPublicKey(modulus, tt.exponent)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantE, key.E)
		})
	}
}

// newKeyServer starts a fake login service that serves keys.
func newKeyServer(t *testing.T, keys ...testKey) *keyServer {
	t.Helper()

	ks := &keyServer{server: fakehttp.New(t), keys: keys}

	ks.server.HandleFunc(http.MethodGet, metadataPath, func(w http.ResponseWriter, _ *http.Request) {
		ks.mu.Lock()
		failing := ks.failing
		ks.mu.Unlock()

		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writeJSON(t, w, map[string]any{
			"issuer":                                BotFrameworkIssuer,
			"jwks_uri":                              ks.server.URL() + keysPath,
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	ks.server.HandleFunc(http.MethodGet, keysPath, func(w http.ResponseWriter, _ *http.Request) {
		ks.mu.Lock()
		defer ks.mu.Unlock()

		ks.fetches++

		set := []map[string]any{}
		for _, k := range ks.keys {
			set = append(set, map[string]any{
				"kty":          "RSA",
				"use":          "sig",
				"kid":          k.id,
				"n":            base64.RawURLEncoding.EncodeToString(k.private.N.Bytes()),
				"e":            base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.private.E)).Bytes()),
				"endorsements": k.endorsements,
			})
		}

		writeJSON(t, w, map[string]any{"keys": set})
	})

	return ks
}

// set makes the server serve keys from now on, unless keys is nil, and
// fail or not.
func (ks *keyServer) set(keys []testKey, failing bool) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if keys != nil {
		ks.keys = keys
	}

	ks.failing = failing
}

// fetchCount returns how often the key set was fetched.
func (ks *keyServer) fetchCount() int {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	return ks.fetches
}

// newTestVerifier returns a verifier for testAppID that reads the keys of
// keys, with its clock at testNow.
func newTestVerifier(t *testing.T, keys *keyServer) *Verifier {
	t.Helper()

	v, err := NewVerifier(VerifierOptions{AppID: testAppID, MetadataURL: keys.server.URL() + metadataPath, HTTP: keys.server.Client()})
	require.NoError(t, err)

	v.now = func() time.Time { return testNow }

	return v
}

// newTestKey makes an RSA key with id, endorsed for the channels.
func newTestKey(t *testing.T, id string, channels ...string) testKey {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, testRSABits)
	require.NoError(t, err)

	return testKey{id: id, private: private, endorsements: channels}
}

// validClaims are the claims of a token that passes every check at
// testNow.
func validClaims() botClaims {
	return botClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    BotFrameworkIssuer,
			Audience:  jwt.ClaimStrings{testAppID},
			IssuedAt:  jwt.NewNumericDate(testNow.Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(testNow.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(testNow.Add(time.Hour)),
		},
		ServiceURL: testServiceURL,
	}
}

// sign signs claims with key, naming the key in the kid header.
func sign(t *testing.T, key testKey, claims botClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = key.id

	signed, err := token.SignedString(key.private)
	require.NoError(t, err)

	return signed
}

// writeJSON answers with v as JSON.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode the answer: %v", err)
	}
}

// orDefault returns value, def for "", or "" for "-".
func orDefault(value, def string) string {
	switch value {
	case "":
		return def
	case "-":
		return ""
	default:
		return value
	}
}

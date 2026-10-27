package teams

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// KeysRefreshInterval is how long the Bot Framework's signing keys are
// used before they are fetched again. Microsoft asks bots to refresh them
// at least once a day, because new keys can be added at any time.
const KeysRefreshInterval = 24 * time.Hour

// UnknownKeyRefreshInterval is the shortest time between two fetches of
// the keys. A token signed with a key the cache does not hold makes the
// cache fetch the keys again, so a key Microsoft just added is found, but
// only this often, so tokens with made-up key IDs cannot make Sidekick
// fetch on every request. A failed refresh waits as long.
const UnknownKeyRefreshInterval = 5 * time.Minute

// maxKeysResponseBytes is the largest metadata or key set read: 1 MiB.
const maxKeysResponseBytes = 1 << 20

// maxExponentBytes is the longest RSA public exponent accepted, short
// enough to fit an int anywhere. Real keys use 65537, which takes 3 bytes.
const maxExponentBytes = 3

// keyTypeRSA is the only key type Bot Framework keys have.
const keyTypeRSA = "RSA"

// keyUseSignature is the use of a signing key, when a key states its use.
const keyUseSignature = "sig"

var (
	// errKeysUnavailable is wrapped when no keys could be fetched. The
	// request can be tried again later.
	errKeysUnavailable = errors.New("the Bot Framework signing keys are unavailable")
	errUnknownKey      = errors.New("the token is signed with an unknown key")
	errNoJWKSURI       = errors.New("the OpenID metadata names no jwks_uri")
	errNoKeys          = errors.New("the key set holds no RSA signing keys")
	errKeysTooLarge    = errors.New("the response is larger than 1 MiB")
	errBadKey          = errors.New("the key's modulus or exponent is out of range")
)

// keyCache holds the Bot Framework's signing keys, by key ID.
type keyCache struct {
	metadataURL string
	client      *http.Client
	log         *slog.Logger

	mu   sync.Mutex
	keys map[string]signingKey
	// fetchedAt is when keys were fetched; triedAt is when a fetch last
	// started, whether it worked or not.
	fetchedAt time.Time
	triedAt   time.Time
}

// signingKey is one of the Bot Framework's keys.
type signingKey struct {
	public *rsa.PublicKey
	// endorsements are the channel IDs, such as msteams, the key may sign
	// tokens for.
	endorsements []string
}

// openIDMetadata is the part of the OpenID metadata document Sidekick
// reads.
type openIDMetadata struct {
	JWKSURI string `json:"jwks_uri"`
}

// jsonWebKey is one key of a JWK set, with the Bot Framework's
// endorsements.
type jsonWebKey struct {
	KeyType      string   `json:"kty"`
	Use          string   `json:"use"`
	KeyID        string   `json:"kid"`
	Modulus      string   `json:"n"`
	Exponent     string   `json:"e"`
	Endorsements []string `json:"endorsements"`
}

// key returns the key with ID kid at time now. It fetches the keys first
// when none are held, when they are older than KeysRefreshInterval, or
// when kid is unknown, but never twice within UnknownKeyRefreshInterval
// once some keys are held. A failed refresh keeps the keys it had. Errors
// wrap errKeysUnavailable when no keys are held at all, and errUnknownKey
// when kid is not among them.
func (c *keyCache) key(ctx context.Context, kid string, now time.Time) (signingKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k, known := c.keys[kid]
	mayRetry := now.Sub(c.triedAt) >= UnknownKeyRefreshInterval
	stale := now.Sub(c.fetchedAt) >= KeysRefreshInterval

	if c.keys == nil || (mayRetry && (stale || !known)) {
		c.triedAt = now

		keys, err := c.fetch(ctx)
		switch {
		case err == nil:
			c.keys, c.fetchedAt = keys, now
		case c.keys == nil:
			return signingKey{}, fmt.Errorf("%w: %w", errKeysUnavailable, err)
		default:
			c.log.WarnContext(ctx, "could not refresh the Bot Framework signing keys; using the cached ones", "fetched_at", c.fetchedAt, "error", err.Error())
		}

		k, known = c.keys[kid]
	}

	if !known {
		return signingKey{}, errUnknownKey
	}

	return k, nil
}

// fetch reads the OpenID metadata and then the key set it names.
func (c *keyCache) fetch(ctx context.Context) (map[string]signingKey, error) {
	var meta openIDMetadata
	if err := c.getJSON(ctx, c.metadataURL, &meta); err != nil {
		return nil, fmt.Errorf("read the OpenID metadata: %w", err)
	}

	if meta.JWKSURI == "" {
		return nil, errNoJWKSURI
	}

	if err := checkSecureURL(meta.JWKSURI); err != nil {
		return nil, fmt.Errorf("jwks_uri: %w", err)
	}

	var set struct {
		Keys []jsonWebKey `json:"keys"`
	}

	if err := c.getJSON(ctx, meta.JWKSURI, &set); err != nil {
		return nil, fmt.Errorf("read the key set: %w", err)
	}

	keys := map[string]signingKey{}

	for _, jwk := range set.Keys {
		if jwk.KeyType != keyTypeRSA || (jwk.Use != "" && jwk.Use != keyUseSignature) || jwk.KeyID == "" {
			continue
		}

		public, err := rsaPublicKey(jwk.Modulus, jwk.Exponent)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", jwk.KeyID, err)
		}

		keys[jwk.KeyID] = signingKey{public: public, endorsements: jwk.Endorsements}
	}

	if len(keys) == 0 {
		return nil, errNoKeys
	}

	return keys, nil
}

// getJSON GETs url and decodes its JSON body into v.
func (c *keyCache) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxKeysResponseBytes, errKeysTooLarge))
	if err != nil {
		return fmt.Errorf("read the response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode the response: %w", err)
	}

	return nil
}

// rsaPublicKey builds an RSA public key from the base64url modulus and
// exponent of a JWK.
func rsaPublicKey(modulus, exponent string) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, fmt.Errorf("decode the modulus: %w", err)
	}

	e, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, fmt.Errorf("decode the exponent: %w", err)
	}

	if len(n) == 0 || len(e) == 0 || len(e) > maxExponentBytes {
		return nil, errBadKey
	}

	exp := 0
	for _, b := range e {
		exp = exp<<8 | int(b)
	}

	if exp < 3 {
		return nil, errBadKey
	}

	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}, nil
}

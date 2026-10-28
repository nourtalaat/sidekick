package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// loginKeyInfo binds the key that seals login state to that one use, so a
// key derived from the same client secret for anything else differs.
const loginKeyInfo = "sidekick oidc login state v1"

// loginKeyBytes is the size of the AES-256 key that seals login state.
const loginKeyBytes = 32

// errBadLoginState means a login cookie does not open, is not for this
// server, or has expired.
var errBadLoginState = errors.New("the login state is missing, not valid or expired")

// loginState is what /auth/login keeps in the browser for /auth/callback:
// the values that tie the provider's answer to this login.
type loginState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Return   string    `json:"return"`
	Expires  time.Time `json:"expires"`
}

// loginKey derives the key that seals login state from the client secret,
// which every replica has, so the callback can land on any of them.
func loginKey(clientSecret string) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, []byte(clientSecret), nil, loginKeyInfo, loginKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("derive the login state key: %w", err)
	}

	return key, nil
}

// seal encrypts and authenticates st with key, so the browser can hold it
// but can neither read nor change it.
func seal(key []byte, st loginState) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", fmt.Errorf("seal the login state: %w", err)
	}

	plain, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("encode the login state: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)

	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(loginCookie))), nil
}

// open returns the login state that seal made of value with key, when it
// has not expired at now.
func open(key []byte, value string, now time.Time) (loginState, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return loginState{}, fmt.Errorf("open the login state: %w", err)
	}

	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return loginState{}, errBadLoginState
	}

	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(loginCookie))
	if err != nil {
		return loginState{}, errBadLoginState
	}

	var st loginState
	if err := json.Unmarshal(plain, &st); err != nil || !now.Before(st.Expires) {
		return loginState{}, errBadLoginState
	}

	return st, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("make the login state cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("make the login state cipher: %w", err)
	}

	return gcm, nil
}

// randomToken returns n random bytes, base64url encoded without padding.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)

	return base64.RawURLEncoding.EncodeToString(b)
}

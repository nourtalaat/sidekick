package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The headers of Sidekick's own HMAC signature scheme.
const (
	HeaderTimestamp = "X-Sidekick-Timestamp"
	HeaderSignature = "X-Sidekick-Signature"
)

const (
	bearerScheme    = "Bearer "
	signaturePrefix = "sha256="
)

var (
	_ Authenticator = Bearer{}
	_ Authenticator = HMAC{}
)

// Bearer checks an Authorization: Bearer token. An empty Token accepts no
// delivery.
type Bearer struct {
	Token []byte
}

// Authenticate implements Authenticator. It compares the token in constant
// time.
func (b Bearer) Authenticate(req Request) error {
	if len(b.Token) == 0 {
		return fmt.Errorf("no token is configured: %w", ErrUnauthenticated)
	}

	header := req.Header.Get("Authorization")
	if len(header) < len(bearerScheme) || !strings.EqualFold(header[:len(bearerScheme)], bearerScheme) {
		return fmt.Errorf("missing bearer token: %w", ErrUnauthenticated)
	}

	if subtle.ConstantTimeCompare([]byte(header[len(bearerScheme):]), b.Token) != 1 {
		return fmt.Errorf("wrong bearer token: %w", ErrUnauthenticated)
	}

	return nil
}

// HMAC checks Sidekick's own signature scheme: X-Sidekick-Timestamp is Unix
// seconds, and X-Sidekick-Signature is "sha256=" followed by the hex
// HMAC-SHA256, keyed with Secret, of the timestamp, a ".", and the body. The
// timestamp must be within Tolerance of the time the delivery arrived, so
// an old delivery cannot be sent again later. An empty Secret accepts no
// delivery.
type HMAC struct {
	Secret    []byte
	Tolerance time.Duration
}

// Authenticate implements Authenticator. It compares the signature in
// constant time.
func (h HMAC) Authenticate(req Request) error {
	if len(h.Secret) == 0 {
		return fmt.Errorf("no HMAC secret is configured: %w", ErrUnauthenticated)
	}

	ts := req.Header.Get(HeaderTimestamp)

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("parse timestamp: %w", ErrUnauthenticated)
	}

	if age := req.ReceivedAt.Sub(time.Unix(sec, 0)); age > h.Tolerance || age < -h.Tolerance {
		return fmt.Errorf("timestamp outside tolerance: %w", ErrUnauthenticated)
	}

	sig, ok := strings.CutPrefix(req.Header.Get(HeaderSignature), signaturePrefix)
	if !ok {
		return fmt.Errorf("missing signature: %w", ErrUnauthenticated)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", ErrUnauthenticated)
	}

	if !hmac.Equal(got, Sign(h.Secret, ts, req.Body)) {
		return fmt.Errorf("wrong signature: %w", ErrUnauthenticated)
	}

	return nil
}

// SignatureHeader returns the X-Sidekick-Signature value for body sent at
// timestamp, a decimal number of Unix seconds.
func SignatureHeader(secret []byte, timestamp string, body []byte) string {
	return signaturePrefix + hex.EncodeToString(Sign(secret, timestamp, body))
}

// Sign computes the raw signature that HMAC expects for body sent at
// timestamp, a decimal number of Unix seconds.
func Sign(secret []byte, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)

	return mac.Sum(nil)
}

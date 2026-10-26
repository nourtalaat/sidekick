//go:generate mockgen -source intake.go -destination mocks_intake.go -package intake -write_package_comment=false

// Package intake turns webhook deliveries into events.
package intake

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
)

// Adapter converts one sender's webhook payload into events (IN-1, IN-2).
// One delivery may hold several alerts, as Alertmanager's does.
type Adapter interface {
	// Parse returns the events in the delivery. Each event must have a Key
	// derived from the payload, so a repeated delivery maps to the same keys.
	Parse(ctx context.Context, req Request) ([]event.Event, error)
}

// SignatureVerifier is implemented by adapters whose sender signs requests in
// its own way. Endpoints without one use the configured HMAC or bearer check.
type SignatureVerifier interface {
	VerifySignature(req Request, secret []byte) error
}

// Authenticator checks a delivery before it is parsed.
type Authenticator interface {
	Authenticate(req Request) error
}

// Request is a webhook delivery after the body has been read (with a size limit).
type Request struct {
	Endpoint   string
	Header     http.Header
	Body       []byte
	ReceivedAt time.Time
}

// ErrUnauthenticated is returned when a delivery fails authentication (IN-3).
var ErrUnauthenticated = errors.New("unauthenticated")

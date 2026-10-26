// Package webhook is the webhook callback output: it posts the final result
// of an investigation, and each watcher state change, as JSON to a URL
// from the config, signed with the same HMAC scheme Sidekick's own webhook intake checks, so a receiver can
// verify a callback the way intake verifies a delivery.
//
// Each request carries X-Sidekick-Timestamp, the Unix seconds it was
// signed at, and X-Sidekick-Signature, "sha256=" and the hex HMAC-SHA256 of
// the timestamp, a "." and the body, keyed with the output's secret. The
// secret itself is never sent. X-Sidekick-Delivery is the same for every
// attempt of one callback, so a receiver can drop repeats.
//
// The URL must be https, except for a loopback host. A redirect is
// followed only when it keeps the method and body (307 or 308) and stays
// on the same scheme and host, so a signed body never goes anywhere else.
package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the output type this package registers. A config selects it with
// "type: webhook".
const Type = "webhook"

// The headers of a callback besides intake's signature headers.
const (
	headerEvent    = "X-Sidekick-Event"
	headerDelivery = "X-Sidekick-Delivery"
)

// opDeliver is the operation of a callback, as
// sidekick_output_api_errors_total names it.
const opDeliver = "deliver"

// maxRedirects is how many same-host redirects one callback follows.
const maxRedirects = 3

// userAgent names Sidekick to the receiver.
const userAgent = "Sidekick-Webhook/1"

// The keys of Ref.Data.
const (
	dataSeq         = "seq"
	dataDeliveredAt = "delivered_at"
)

var (
	errNoURL         = errors.New("url: required")
	errNoSecret      = errors.New("hmacSecret: required")
	errNoSecrets     = errors.New("no secrets resolver to read the HMAC secret with")
	errEmptySecret   = errors.New("hmacSecret: the secret is empty")
	errOtherHost     = errors.New("the callback URL redirects to another scheme or host")
	errMethodChange  = errors.New("the callback URL redirects with a status that drops the body; only 307 and 308 are followed")
	errManyRedirects = fmt.Errorf("the callback URL redirects more than %d times", maxRedirects)
)

// init registers the output type when the package is linked in.
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one webhook callback output.
type Config struct {
	// URL receives the callbacks. It must be https, except for a loopback
	// host such as localhost.
	URL string `yaml:"url"`
	// HMACSecret is the key the callbacks are signed with. The receiver
	// needs the same secret to check them.
	HMACSecret registry.SecretRef `yaml:"hmacSecret"`
}

// Validate checks the config without secrets or network calls.
func (c *Config) Validate() error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errNoURL)
	} else {
		errs = append(errs, sinkhttp.CheckURL("url", c.URL))
	}

	if c.HMACSecret == (registry.SecretRef{}) {
		errs = append(errs, errNoSecret)
	}

	return errors.Join(errs...)
}

// Sink posts signed callbacks to one URL.
type Sink struct {
	name   string
	url    string
	secret func(ctx context.Context) ([]byte, error)
	http   *sinkhttp.Client
	log    *slog.Logger
	// now is the clock. Tests replace it.
	now func() time.Time
}

var _ output.Sink = (*Sink)(nil)

// Accepts reports whether the sink shows updates of phase: it sends the
// final result and watcher state changes.
func (s *Sink) Accepts(phase output.Phase) bool {
	return phase == output.PhaseFinal || phase == output.PhaseWatcher
}

// Publish sends the callback of u. An update whose Seq is not newer than
// the last one delivered is dropped, and so is a watcher update that does
// not say which watcher changed. A failed delivery, a status outside 2xx
// included, returns an error, so the publish job tries again with the
// same delivery ID and a fresh signature.
func (s *Sink) Publish(ctx context.Context, u output.Update) (output.Ref, error) {
	if u.Previous != nil {
		if seq, err := strconv.ParseInt(u.Previous.Data[dataSeq], 10, 64); err == nil && u.Seq <= seq {
			s.log.DebugContext(ctx, "update dropped: a newer one was delivered", "investigation_id", u.InvestigationID, "seq", u.Seq, "delivered_seq", seq)

			return *u.Previous, nil
		}
	}

	if u.Phase == output.PhaseWatcher && u.Watcher == nil {
		s.log.WarnContext(ctx, "watcher update dropped: it does not say which watcher changed", "investigation_id", u.InvestigationID, "seq", u.Seq)

		return s.skipped(u), nil
	}

	p := newPayload(u)

	body, err := p.encode()
	if err != nil {
		return output.Ref{}, fmt.Errorf("encode the callback: %w", err)
	}

	secret, err := s.secret(ctx)
	if err != nil {
		return output.Ref{}, fmt.Errorf("read the HMAC secret: %w", err)
	}

	resp, err := s.http.Do(ctx, opDeliver, func(ctx context.Context) (*http.Request, error) {
		return s.request(ctx, p, body, secret)
	})
	if err != nil {
		return output.Ref{}, fmt.Errorf("deliver the callback: %w", err)
	}

	if err := resp.Err(); err != nil {
		return output.Ref{}, fmt.Errorf("deliver the callback: the receiver answered %w", err)
	}

	s.log.DebugContext(ctx, "callback delivered", "investigation_id", u.InvestigationID, "delivery", p.DeliveryID, "status", resp.Status)

	return output.Ref{
		Sink:     s.name,
		External: p.DeliveryID,
		Data: map[string]string{
			dataSeq:         strconv.FormatInt(u.Seq, 10),
			dataDeliveredAt: strconv.FormatInt(s.now().UnixMilli(), 10),
		},
	}, nil
}

// skipped returns the ref of an update that sent nothing: the previous
// delivery, now showing u.Seq so the update is not tried again.
func (s *Sink) skipped(u output.Update) output.Ref {
	ref := output.Ref{Sink: s.name, Data: map[string]string{}}
	if u.Previous != nil {
		ref.External = u.Previous.External
		maps.Copy(ref.Data, u.Previous.Data)
	}

	ref.Data[dataSeq] = strconv.FormatInt(u.Seq, 10)

	return ref
}

// request makes one signed attempt of the callback, with a timestamp of
// its own.
func (s *Sink) request(ctx context.Context, p payload, body, secret []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("make the request: %w", err)
	}

	ts := strconv.FormatInt(s.now().Unix(), 10)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set(headerEvent, p.Event)
	req.Header.Set(headerDelivery, p.DeliveryID)
	req.Header.Set(intake.HeaderTimestamp, ts)
	req.Header.Set(intake.HeaderSignature, intake.SignatureHeader(secret, ts, body))

	return req, nil
}

// New builds a webhook callback output. It reads the HMAC secret through
// deps.Secrets on every callback and sends with a copy of deps.HTTP, or of
// a client from httpclient.New when it is nil, that follows only
// same-host redirects that keep the body.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	if deps.Secrets == nil {
		return nil, fmt.Errorf("webhook output %q: %w", name, errNoSecrets)
	}

	base := deps.HTTP
	if base == nil {
		base = httpclient.New()
	}

	hc := *base
	hc.CheckRedirect = sameHostRedirect

	sc, err := sinkhttp.New(Type, &hc, deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("webhook output %q: %w", name, err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	secret := func(ctx context.Context) ([]byte, error) {
		value, err := secrets.ResolveText(ctx, deps.Secrets, cfg.HMACSecret)
		if err != nil {
			return nil, fmt.Errorf("read hmacSecret: %w", err)
		}

		if value == "" {
			return nil, errEmptySecret
		}

		return []byte(value), nil
	}

	return &Sink{
		name:   name,
		url:    cfg.URL,
		secret: secret,
		http:   sc,
		log:    log.With("component", "output", "sink", name),
		now:    time.Now,
	}, nil
}

// sameHostRedirect follows a 307 or 308 redirect to the same scheme and
// host, at most maxRedirects times, and refuses every other redirect, so
// the signed body never reaches another host and a POST is never turned
// into a GET that would look delivered.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	first := via[0].URL

	switch {
	case len(via) > maxRedirects:
		return errManyRedirects
	case req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host):
		return errOtherHost
	case req.Response == nil ||
		(req.Response.StatusCode != http.StatusTemporaryRedirect && req.Response.StatusCode != http.StatusPermanentRedirect):
		return errMethodChange
	default:
		return nil
	}
}

// Package generic is the webhook adapter for senders that have no adapter
// of their own. It accepts any JSON object and maps its fields to one
// event through the JSON paths in the endpoint's fields setting.
package generic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Adapter is the name config selects this adapter with, and the sender
// type in fingerprints.
const Adapter = "generic"

// serviceLabel is the label name the service takes in the fingerprint.
const serviceLabel = "service"

var (
	errTitleRequired = errors.New("fields.title: required")
	errNotFound      = errors.New("finds nothing in the payload")
	errManyValues    = errors.New("finds more than one value; use an index such as [0] instead of [*]")
	errNotScalar     = errors.New("expected a string, a number or true or false")
	errNotObject     = errors.New("expected an object")
)

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config.
type Config struct {
	Fields Fields `yaml:"fields"`
}

// Fields are the JSON paths that find each part of the event in the
// payload. Only Title is required.
type Fields struct {
	// Title is a short summary of the alert.
	Title jsonpath.Path `yaml:"title"`
	// Text is the longer description.
	Text jsonpath.Path `yaml:"text"`
	// Service is the affected service.
	Service jsonpath.Path `yaml:"service"`
	// Severity is the sender's severity name, such as critical or warning.
	Severity jsonpath.Path `yaml:"severity"`
	// Key identifies the alert, so a repeated delivery is stored once. Without
	// it, the key is the SHA-256 of the whole body.
	Key jsonpath.Path `yaml:"key"`
	// Labels is an object whose members become the event's labels.
	Labels jsonpath.Path `yaml:"labels"`
}

// Validate checks that the title path is set. The decoder has already
// parsed every path.
func (c *Config) Validate() error {
	if c.Fields.Title.IsZero() {
		return errTitleRequired
	}

	return nil
}

// Generic maps a JSON payload to one event.
type Generic struct {
	fields Fields
}

var _ intake.Adapter = (*Generic)(nil)

// New builds the adapter from its validated config.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (intake.Adapter, error) {
	return &Generic{fields: cfg.Fields}, nil
}

// Parse implements intake.Adapter. It returns one alert event. A path that
// finds nothing leaves its part empty, except for title and key, which
// must be found once they are configured. Numbers and true or false are
// taken as text; objects and arrays are errors, except for labels, which
// must be an object of such values. Errors name the field and its path,
// never a value from the payload.
func (g *Generic) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	dec := json.NewDecoder(bytes.NewReader(req.Body))
	dec.UseNumber()

	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode the payload as JSON: %w", err)
	}

	if dec.More() {
		return nil, errors.New("the payload holds more than one JSON value")
	}

	if _, ok := doc.(map[string]any); !ok {
		return nil, errors.New("the payload must be a JSON object")
	}

	var p parser

	ev := event.Event{
		Kind:       event.KindAlert,
		Sender:     event.Sender{Kind: event.SenderSystem},
		Title:      p.text(doc, "title", g.fields.Title, true),
		Text:       p.text(doc, "text", g.fields.Text, false),
		Service:    p.text(doc, "service", g.fields.Service, false),
		Severity:   intake.ParseSeverity(p.text(doc, "severity", g.fields.Severity, false)),
		Key:        p.text(doc, "key", g.fields.Key, true),
		Labels:     p.labels(doc, g.fields.Labels),
		ReceivedAt: req.ReceivedAt,
		Raw:        json.RawMessage(req.Body),
	}

	if err := errors.Join(p.errs...); err != nil {
		return nil, fmt.Errorf("map fields: %w", err)
	}

	if g.fields.Key.IsZero() {
		sum := sha256.Sum256(req.Body)
		ev.Key = hex.EncodeToString(sum[:])
	}

	ev.Fingerprint = fingerprint(ev)

	return []event.Event{ev}, nil
}

// parser collects the errors of the fields it reads, so one response names
// every field that is wrong.
type parser struct {
	errs []error
}

// text returns the one value path finds in doc as text. A path that is not
// set gives "". A required path must find a value.
func (p *parser) text(doc any, field string, path jsonpath.Path, required bool) string {
	if path.IsZero() {
		return ""
	}

	v, ok, err := single(doc, path)

	switch {
	case err != nil:
	case !ok && required:
		err = errNotFound
	case ok:
		if s, isScalar := scalar(v); isScalar {
			return s
		}

		err = errNotScalar
	}

	p.add(field, path, err)

	return ""
}

// labels returns the members of the object path finds, as text. Members
// that are null are left out.
func (p *parser) labels(doc any, path jsonpath.Path) map[string]string {
	labels := map[string]string{}
	if path.IsZero() {
		return labels
	}

	v, ok, err := single(doc, path)
	if err != nil || !ok {
		p.add("labels", path, err)

		return labels
	}

	obj, isObj := v.(map[string]any)
	if !isObj {
		p.add("labels", path, errNotObject)

		return labels
	}

	for _, name := range slices.Sorted(maps.Keys(obj)) {
		if obj[name] == nil {
			continue
		}

		s, isScalar := scalar(obj[name])
		if !isScalar {
			p.add("labels", path, fmt.Errorf("label %q: %w", name, errNotScalar))

			continue
		}

		labels[name] = s
	}

	return labels
}

// add records err for field, when there is one.
func (p *parser) add(field string, path jsonpath.Path, err error) {
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("fields.%s (%s): %w", field, path, err))
	}
}

// single returns the one value path finds in doc. A path that finds
// nothing, or only null, gives ok false; one that finds more than one
// value is an error.
func single(doc any, path jsonpath.Path) (v any, ok bool, err error) {
	found := path.Find(doc)

	switch {
	case len(found) > 1:
		return nil, false, errManyValues
	case len(found) == 0 || found[0] == nil:
		return nil, false, nil
	default:
		return found[0], true, nil
	}
}

// scalar returns a string, number or boolean as text.
func scalar(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}

// fingerprint covers the title, the service and every label, because the
// generic payload has no alert identity of its own.
func fingerprint(ev event.Event) string {
	labels := maps.Clone(ev.Labels)
	if _, ok := labels[serviceLabel]; !ok && ev.Service != "" {
		labels[serviceLabel] = ev.Service
	}

	return event.Fingerprint(Adapter, ev.Title, labels, slices.Collect(maps.Keys(labels)))
}

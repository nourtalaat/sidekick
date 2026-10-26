package config

import (
	"errors"
	"reflect"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// Keys the loader reads itself from plugin sections.
const (
	typeKey     = "type"
	providerKey = "provider"
	adapterKey  = "adapter"
)

// errNotPointer is returned when Section.Decode is given something it cannot
// decode into.
var errNotPointer = errors.New("decode target must be a non-nil pointer")

// Section is one plugin instance's own part of the config, such as one entry
// under sources. The loader reads only the plugin type itself (the type key,
// or provider for model connectors, or adapter for webhooks). Node holds the
// remaining keys, which the plugin decodes into its own config struct
// through registry.Typed.
type Section struct {
	// Type is the registered plugin type the section names.
	Type string
	// Node is the section without the keys the loader reads itself.
	Node *yaml.Node

	path string
}

var _ registry.Decoder = Section{}

// Path returns the field path of the section, for example sources.k8s.
func (s Section) Path() string {
	return s.path
}

// Decode implements registry.Decoder. It decodes the section strictly:
// unknown keys are errors. On failure it returns Errors, with paths that
// start with the section's own path.
func (s Section) Decode(target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return errNotPointer
	}

	var errs errorList

	d := decoder{errs: &errs}
	d.decode(s.Node, v.Elem(), s.path)

	return errs.err()
}

// decodeNode reads the type key and keeps the rest of the node.
func (s *Section) decodeNode(d *decoder, node *yaml.Node, path string) {
	s.split(d, node, path, typeKey)
}

// split reads typeName from a plugin section, keeps the keys that belong to
// the plugin in s.Node and returns the mapping of the owned keys, which the
// loader decodes itself.
func (s *Section) split(d *decoder, node *yaml.Node, path, typeName string, owned ...string) *yaml.Node {
	s.path = path
	s.Node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: node.Line, Column: node.Column}
	own := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: node.Line, Column: node.Column}

	if node.Kind != yaml.MappingNode {
		d.fail(path, "expected a mapping, got %s", describe(node))

		return own
	}

	for _, p := range d.pairs(node, path) {
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.key}

		switch {
		case p.key == typeName:
			d.decode(p.value, reflect.ValueOf(&s.Type).Elem(), joinPath(path, p.key))
		case slices.Contains(owned, p.key):
			own.Content = append(own.Content, key, p.value)
		default:
			s.Node.Content = append(s.Node.Content, key, p.value)
		}
	}

	return own
}

// Connector is one entry under model.connectors. Its Type is the provider.
type Connector struct {
	Section
}

// decodeNode reads the provider key and keeps the rest for the connector.
func (c *Connector) decodeNode(d *decoder, node *yaml.Node, path string) {
	c.split(d, node, path, providerKey)
}

// Webhook is one entry under intake.webhooks: an endpoint at
// /hooks/<name>. Its Type is the adapter. Authentication and trust belong to
// the endpoint, so the loader decodes them; the adapter gets the other keys.
type Webhook struct {
	Section `yaml:"-"`

	// Auth checks every delivery before it is parsed (IN-3).
	Auth WebhookAuth `yaml:"auth"`
	// Trusted marks events from this endpoint as coming from a trusted
	// sender (ACT-6). It comes from config, never from the payload.
	Trusted bool `yaml:"trusted"`
}

// decodeNode reads the adapter, auth and trusted keys and keeps the rest for
// the adapter.
func (w *Webhook) decodeNode(d *decoder, node *yaml.Node, path string) {
	own := w.split(d, node, path, adapterKey, "auth", "trusted")
	d.decodeStruct(own, reflect.ValueOf(w).Elem(), path)
}

// WebhookAuth is how a webhook endpoint authenticates deliveries. Exactly
// one of Bearer and HMAC is set.
type WebhookAuth struct {
	// Bearer is the expected Authorization: Bearer token.
	Bearer *registry.SecretRef `yaml:"bearer"`
	// HMAC is the key of Sidekick's own HMAC signature scheme.
	HMAC *registry.SecretRef `yaml:"hmac"`
	// Tolerance is how far a signed timestamp may be from the time a
	// delivery arrives. It applies to HMAC only.
	Tolerance time.Duration `yaml:"tolerance"`
}

func (w *Webhook) setDefaults() {
	w.Auth.setDefaults()
}

func (a *WebhookAuth) setDefaults() {
	a.Tolerance = DefaultHMACTolerance
}

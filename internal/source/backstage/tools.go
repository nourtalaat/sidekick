package backstage

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example backstage__get_service.
const (
	toolGetService = "get_service"
	toolGetOwner   = "get_owner"
	toolGetSystem  = "get_system"
)

// The kinds each tool reads.
var (
	serviceKinds = []string{kindComponent, kindResource, kindAPI}
	ownerKinds   = []string{kindGroup, kindUser}
	systemKinds  = []string{kindSystem}
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolGetService,
		Description: "Look up a service in the Backstage catalog: its owner, the system it is part of, its type and " +
			"lifecycle, the services and resources it depends on, the ones that depend on it, the APIs it provides " +
			"and consumes, and its links.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"service": stringProperty("The service name, such as the service an alert names, or an entity ref such as " +
				"component:default/checkout-api or resource:default/orders-db. A bare name is a component."),
		}, "service"),
		ReadOnly: true,
	},
	{
		Name: toolGetOwner,
		Description: "Look up a team or person in the Backstage catalog, such as the owner get_service returns: " +
			"name, email, parent and child teams, members, and what it owns.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"owner": stringProperty("The team name or an entity ref such as group:default/payments or user:default/ada. " +
				"A bare name is a group."),
		}, "owner"),
		ReadOnly: true,
	},
	{
		Name: toolGetSystem,
		Description: "Look up a system in the Backstage catalog: its owner, its domain, and the components, " +
			"resources and APIs that are part of it.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"system": stringProperty("The system name, or an entity ref such as system:default/payments."),
		}, "system"),
		ReadOnly: true,
	},
}

// serviceInput is the input of get_service.
type serviceInput struct {
	Service string `json:"service"`
}

// ownerInput is the input of get_owner.
type ownerInput struct {
	Owner string `json:"owner"`
}

// systemInput is the input of get_system.
type systemInput struct {
	System string `json:"system"`
}

// schema returns the JSON Schema of a tool's input. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	s := &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(props)),
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("backstage: encode a tool schema: %v", err))
	}

	return data
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

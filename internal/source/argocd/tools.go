package argocd

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example argocd__list_applications.
const (
	toolListApplications = "list_applications"
	toolGetApplication   = "get_application"
	toolGetHistory       = "get_history"
)

// Limits on how many applications list_applications returns.
const (
	defaultApplications = 50
	maxApplications     = 200
)

// Values of the state filter of list_applications.
const (
	stateAny        = "any"
	stateOutOfSync  = "out_of_sync"
	stateNotHealthy = "not_healthy"
	stateProblem    = "problem"
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolListApplications,
		Description: "List the Argo CD applications Sidekick may read: name, project, destination (cluster and " +
			"namespace), sync status, health, whether auto-sync is on, the revision deployed now, the revision the " +
			"application was last compared with, when it last synced, and a link. Use it to find the application " +
			"of a service, or to see which applications are out of sync or not healthy. Pass the application field " +
			"of a result, as project/name, to the other tools.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"project": stringProperty("Only applications of this project."),
			"name": stringProperty("Only applications whose name matches this pattern, for example checkout-*. " +
				"* matches any run of characters and ? one character."),
			"state": {
				Type: "string",
				Description: "Only applications in this state: out_of_sync, not_healthy (health is not Healthy), " +
					"problem (out of sync or not healthy), or any. Default any.",
				Enum: []any{stateAny, stateOutOfSync, stateNotHealthy, stateProblem},
			},
			"limit": {
				Type:        "integer",
				Description: fmt.Sprintf("The most applications to return, 1 to %d. Default %d.", maxApplications, defaultApplications),
				Minimum:     ptr(1),
				Maximum:     ptr(maxApplications),
			},
		}),
		ReadOnly: true,
	},
	{
		Name: toolGetApplication,
		Description: fmt.Sprintf("Get one Argo CD application: where its manifests come from (repository, path, target "+
			"revision, chart or Kustomize settings), where it deploys to, its project, its sync policy and so whether "+
			"auto-sync is on, its sync status and health, the revision deployed now and the one it was last compared "+
			"with, the last operation (phase, message, times, who started it), its conditions, the images it runs, and "+
			"up to %d resources that are not synced or not healthy.", maxResources),
		InputSchema: schema(map[string]*jsonschema.Schema{
			"application": applicationProperty(),
		}, "application"),
		ReadOnly: true,
	},
	{
		Name: toolGetHistory,
		Description: fmt.Sprintf("Get the deployment history of one Argo CD application, newest first: the history ID, the "+
			"revision, when it was deployed, who started it (a user, or automated), the source, and for each of the "+
			"newest %d the commit message, author and date of its revision. The entry marked current is what runs now. "+
			"Argo CD keeps as many entries as the application's revisionHistoryLimit, 10 by default, so do not look for "+
			"older ones.", maxCommits),
		InputSchema: schema(map[string]*jsonschema.Schema{
			"application": applicationProperty(),
		}, "application"),
		ReadOnly: true,
	},
}

// schema returns the JSON Schema of a tool's input: the tool's own
// properties and the required ones. It panics when the schema does not
// encode, which is a programming error found by any test.
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
		panic(fmt.Sprintf("argocd: encode a tool schema: %v", err))
	}

	return data
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// applicationProperty returns the property that names an application.
func applicationProperty() *jsonschema.Schema {
	return stringProperty("The application as project/name, for example shop/checkout. Take it from the application " +
		"field of list_applications.")
}

// ptr returns a pointer to n as a float64, for schema bounds.
func ptr(n int) *float64 {
	f := float64(n)

	return &f
}

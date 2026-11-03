package gitlab

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example gitlab__list_commits.
const (
	toolListCommits = "list_commits"
	toolGetCommit   = "get_commit"
	toolGetFile     = "get_file"
	toolCompare     = "compare"
)

// Limits on how many items a list tool returns.
const (
	// defaultLimit is the number of items a list returns when the call
	// gives no limit.
	defaultLimit = 30
	// maxLimit is the most items a list returns.
	maxLimit = 100
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolListCommits,
		Description: "List the commits to a GitLab project since a time, newest first, optionally only those on a " +
			"branch or that touch a path. Returns each commit's SHA, message, author, date and link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"since": timeProperty("Only commits made at or after this time."),
			"until": timeProperty("Only commits made before this time."),
			"path":  stringProperty("Only commits that touch this file or directory, for example deploy/values.yaml."),
			"ref":   stringProperty("The branch, tag or SHA to list from. Default: the default branch."),
			"limit": limitProperty(),
		}, "since"),
		ReadOnly: true,
	},
	{
		Name: toolGetCommit,
		Description: "Get one commit of a GitLab project: its message, author, date, parents, the files it " +
			"changed with line counts, and the patch of each file, cut when the commit is large.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"ref": stringProperty("The commit SHA, or a branch or tag to get its latest commit."),
		}, "ref"),
		ReadOnly: true,
	},
	{
		Name: toolGetFile,
		Description: "Read a file of a GitLab project at a ref, or list a directory. Files over 256 KiB are " +
			"cut, and binary files are not shown.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"path": stringProperty("The file or directory, relative to the project root, for example deploy/values.yaml."),
			"ref":  stringProperty("The branch, tag or commit SHA to read at. Default: the default branch."),
		}, "path"),
		ReadOnly: true,
	},
	{
		Name: toolCompare,
		Description: "Compare two refs of a GitLab project, base...head: the newest commits of head that base " +
			"lacks, and the files changed with line counts.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"base":  stringProperty("The older branch, tag or SHA, for example the tag of the last good release."),
			"head":  stringProperty("The newer branch, tag or SHA."),
			"limit": limitProperty(),
		}, "base", "head"),
		ReadOnly: true,
	},
}

// schema returns the JSON Schema of a tool's input: the target properties,
// the tool's own properties and the required ones. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	all := map[string]*jsonschema.Schema{
		"project": stringProperty("The project's full path, for example acme/checkout or acme/platform/checkout. " +
			"Give project or service, not both."),
		"service": stringProperty("A service name, such as the service an alert names; Sidekick looks up its " +
			"project in its own settings. Give project or service, not both."),
	}
	maps.Copy(all, props)

	s := &jsonschema.Schema{
		Type:                 "object",
		Properties:           all,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(all)),
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("gitlab: encode a tool schema: %v", err))
	}

	return data
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// timeProperty returns a property that holds an RFC 3339 time.
func timeProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Format:      "date-time",
		Description: strings.TrimSuffix(description, ".") + ", in RFC 3339 form, for example 2026-05-04T10:00:00Z.",
	}
}

// limitProperty returns the limit property of a list tool.
func limitProperty() *jsonschema.Schema {
	minimum, maximum := 1.0, float64(maxLimit)

	return &jsonschema.Schema{
		Type:        "integer",
		Description: fmt.Sprintf("The most items to return, %d to %d. Default %d.", 1, maxLimit, defaultLimit),
		Minimum:     &minimum,
		Maximum:     &maximum,
	}
}

package github

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
// source prefix, for example github__list_commits.
const (
	toolListCommits      = "list_commits"
	toolGetCommit        = "get_commit"
	toolListPullRequests = "list_pull_requests"
	toolListReleases     = "list_releases"
	toolListWorkflowRuns = "list_workflow_runs"
	toolGetFile          = "get_file"
	toolCompare          = "compare"
)

// Limits on how many items a list tool returns.
const (
	// defaultLimit is the number of items a list returns when the call
	// gives no limit.
	defaultLimit = 30
	// maxLimit is the most items a list returns.
	maxLimit = 100
)

// workflowStatuses are the run statuses and conclusions GitHub can filter
// workflow runs by.
var workflowStatuses = []any{
	"completed", "action_required", "cancelled", "failure", "neutral", "skipped", "stale", "success",
	"timed_out", "in_progress", "queued", "requested", "waiting", "pending",
}

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolListCommits,
		Description: "List the commits to a GitHub repository since a time, newest first, optionally only those on a " +
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
		Description: "Get one commit of a GitHub repository: its message, author, date, parents, the files it " +
			"changed with line counts, and the patch of each file, cut when the commit is large.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"ref": stringProperty("The commit SHA, or a branch or tag to get its latest commit."),
		}, "ref"),
		ReadOnly: true,
	},
	{
		Name: toolListPullRequests,
		Description: "List the pull requests of a GitHub repository that were opened or merged since a time, " +
			"most recently updated first, with their state, branches, author, times, merge commit and link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"since": timeProperty("Only pull requests opened or merged at or after this time."),
			"base":  stringProperty("Only pull requests into this branch, for example main."),
			"limit": limitProperty(),
		}, "since"),
		ReadOnly: true,
	},
	{
		Name: toolListReleases,
		Description: "List the releases of a GitHub repository, newest first, with tag, name, dates and link. " +
			"With includeTags, also list the newest tags and the commit each points at; tags have no dates.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"since":       timeProperty("Only releases made at or after this time."),
			"includeTags": {Type: "boolean", Description: "Also list the newest tags. Default false."},
			"limit":       limitProperty(),
		}),
		ReadOnly: true,
	},
	{
		Name: toolListWorkflowRuns,
		Description: "List the GitHub Actions workflow runs of a repository, newest first, for example deploys: " +
			"workflow, status, conclusion, event, branch, commit, times and link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"since":    timeProperty("Only runs created at or after this time."),
			"workflow": stringProperty("Only runs of this workflow: its file name, such as deploy.yml, or its numeric ID."),
			"branch":   stringProperty("Only runs for this branch."),
			"event":    stringProperty("Only runs started by this event, such as push, workflow_dispatch or release."),
			"status":   {Type: "string", Description: "Only runs with this status or conclusion.", Enum: workflowStatuses},
			"limit":    limitProperty(),
		}),
		ReadOnly: true,
	},
	{
		Name: toolGetFile,
		Description: "Read a file of a GitHub repository at a ref, or list a directory. Files over 256 KiB are " +
			"cut, and binary files are not shown.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"path": stringProperty("The file or directory, relative to the repository root, for example deploy/values.yaml."),
			"ref":  stringProperty("The branch, tag or commit SHA to read at. Default: the default branch."),
		}, "path"),
		ReadOnly: true,
	},
	{
		Name: toolCompare,
		Description: "Compare two refs of a GitHub repository, base...head: how far head is ahead of and behind " +
			"base, the newest commits between them, and the files changed with line counts.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"base":  stringProperty("The older branch, tag or SHA, for example the tag of the last good release."),
			"head":  stringProperty("The newer branch, tag or SHA."),
			"limit": limitProperty(),
		}, "base", "head"),
		ReadOnly: true,
	},
}

// target names the repository of a call: by name, or by a service that
// serviceRepos maps to one. Every tool's input embeds it.
type target struct {
	Repo    string `json:"repo"`
	Service string `json:"service"`
}

// repo is a repository the config allows.
type repo struct {
	owner string
	name  string
}

// String returns owner/name.
func (r repo) String() string {
	return r.owner + "/" + r.name
}

// schema returns the JSON Schema of a tool's input: the target properties,
// the tool's own properties and the required ones. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	all := map[string]*jsonschema.Schema{
		"repo": stringProperty("The repository as owner/name, for example acme/checkout. Give repo or service, not both."),
		"service": stringProperty("A service name, such as the service an alert names; Sidekick looks up its " +
			"repository in its own settings. Give repo or service, not both."),
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
		panic(fmt.Sprintf("github: encode a tool schema: %v", err))
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

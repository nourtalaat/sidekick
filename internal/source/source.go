// Package source defines investigation sources: MCP servers, native
// integrations, Sidekick's memory and the benchmark's mock source.
package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"time"
)

// Source exposes tools the investigation can call.
type Source interface {
	// Tools lists the tools the source offers, before allowlist filtering.
	Tools(ctx context.Context) ([]Tool, error)
	// Call runs one tool. A tool that ran but found nothing returns a Result with NoData set, not an error.
	Call(ctx context.Context, call Call) (Result, error)
	// Close releases connections and child processes.
	Close() error
}

// Tool describes one tool as the source names it.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object.
	InputSchema json.RawMessage
	// ReadOnly is false when the source says the tool can change something.
	// Such tools are never exposed to the model; changes go through actions.
	ReadOnly bool
}

// Call is one tool invocation.
type Call struct {
	ID              string
	Tool            string
	Input           json.RawMessage
	InvestigationID string
	Deadline        time.Time
}

// EvidenceKind says what an evidence link points at.
type EvidenceKind string

// Evidence kinds.
const (
	EvidenceLogQuery  EvidenceKind = "log_query"
	EvidenceMetric    EvidenceKind = "metric"
	EvidenceTrace     EvidenceKind = "trace"
	EvidenceCommit    EvidenceKind = "commit"
	EvidenceDeploy    EvidenceKind = "deploy"
	EvidenceTicket    EvidenceKind = "ticket"
	EvidenceResource  EvidenceKind = "resource"
	EvidenceIncident  EvidenceKind = "past_incident"
	EvidenceMessage   EvidenceKind = "message"
	EvidenceDashboard EvidenceKind = "dashboard"
	EvidenceOther     EvidenceKind = "other"
)

// Evidence is a link a human can open to check a claim.
type Evidence struct {
	Kind  EvidenceKind `json:"kind"`
	Title string       `json:"title"`
	URL   string       `json:"url,omitempty"`
	// ToolCallID ties the evidence to the step that produced it.
	ToolCallID string    `json:"toolCallId,omitempty"`
	At         time.Time `json:"at,omitzero"`
}

// Result is the answer to one Call.
type Result struct {
	Text       string
	Structured json.RawMessage
	Evidence   []Evidence
	NoData     bool
	Truncated  bool
}

// Filter is a per-source tool allowlist and denylist (SRC-2). Patterns use path.Match syntax.
// An empty Allow list allows every tool; Deny always wins.
type Filter struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// Allows reports whether the tool name passes the filter.
func (f Filter) Allows(name string) (bool, error) {
	for _, p := range f.Deny {
		ok, err := path.Match(p, name)
		if err != nil {
			return false, fmt.Errorf("deny pattern %q: %w", p, err)
		}

		if ok {
			return false, nil
		}
	}

	if len(f.Allow) == 0 {
		return true, nil
	}

	for _, p := range f.Allow {
		ok, err := path.Match(p, name)
		if err != nil {
			return false, fmt.Errorf("allow pattern %q: %w", p, err)
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
}

// Separator joins a source name and a tool name in the name the model sees.
const Separator = "__"

// MaxToolNameLen is the shortest tool name limit across supported providers.
const MaxToolNameLen = 64

var (
	sourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	toolNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

	// ErrBadName is returned for names the providers would refuse.
	ErrBadName = errors.New("invalid name")
)

// QualifiedName returns the namespaced tool name, for example "k8s__get_pod_events".
func QualifiedName(sourceName, toolName string) (string, error) {
	if !sourceNamePattern.MatchString(sourceName) {
		return "", fmt.Errorf("source %q: %w", sourceName, ErrBadName)
	}

	if !toolNamePattern.MatchString(toolName) {
		return "", fmt.Errorf("tool %q of source %q: %w", toolName, sourceName, ErrBadName)
	}

	name := sourceName + Separator + toolName
	if len(name) > MaxToolNameLen {
		return "", fmt.Errorf("tool %q is longer than %d characters: %w", name, MaxToolNameLen, ErrBadName)
	}

	return name, nil
}

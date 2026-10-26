package postgres

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example db__slow_queries.
const (
	toolRunningQueries = "running_queries"
	toolLockWaits      = "lock_waits"
)

// Limits on how many rows a tool returns.
const (
	// defaultLimit is the number of rows a tool returns when the call gives
	// no limit.
	defaultLimit = 20
	// maxLimit is the most rows a tool returns.
	maxLimit = 100
	// maxDurationMs is the longest minimum duration a call may ask for, a
	// day.
	maxDurationMs = 24 * 60 * 60 * 1000
)

// input is the input of every tool. Each tool reads the fields its schema
// has.
type input struct {
	Database      string `json:"database"`
	Limit         int    `json:"limit"`
	MinDurationMs int64  `json:"minDurationMs"`
	MinWaitMs     int64  `json:"minWaitMs"`
}

// limit returns the limit the call gives, or defaultLimit, within 1 and
// maxLimit.
func (in input) limit() int {
	if in.Limit <= 0 {
		return defaultLimit
	}

	return min(in.Limit, maxLimit)
}

// toolList returns the source's tools. The database property lists the
// configured names, so the guard refuses any other before a call is made.
// Every tool only reads.
func toolList(databases []string) []source.Tool {
	return []source.Tool{
		{
			Name: toolRunningQueries,
			Description: "List the sessions of a Postgres database that are running a query or holding a " +
				"transaction open, longest running first, from pg_stat_activity: user, application, client address, " +
				"state, wait event, how long the query and the transaction have run, the sessions blocking it, and " +
				"the query's text.",
			InputSchema: schema(databases, map[string]*jsonschema.Schema{
				"minDurationMs": durationProperty("Only sessions whose query or open transaction has run at least this many milliseconds. Default 0."),
				"limit":         limitProperty(),
			}),
			ReadOnly: true,
		},
		{
			Name: toolLockWaits,
			Description: "List the sessions of a Postgres database that are waiting for a lock, longest waiting " +
				"first, from pg_locks and pg_stat_activity: the lock, how long each has waited, and the sessions " +
				"blocking it. Also lists every blocking session, and the ones at the head of each blocking chain, " +
				"which block others without waiting themselves.",
			InputSchema: schema(databases, map[string]*jsonschema.Schema{
				"minWaitMs": durationProperty("Only sessions that have waited at least this many milliseconds. Default 0."),
				"limit":     limitProperty(),
			}),
			ReadOnly: true,
		},
	}
}

// decode decodes a tool's input. The guard has checked it against the
// tool's schema already, so a failure means the schema and the input type
// disagree; it still goes back to the model as a tool error.
func decode(raw json.RawMessage) (input, error) {
	var in input

	if len(raw) == 0 {
		return in, nil
	}

	if err := json.Unmarshal(raw, &in); err != nil {
		return in, &source.ToolError{Message: "the input does not decode: " + err.Error()}
	}

	return in, nil
}

// schema returns the JSON Schema of a tool's input: the database property,
// the tool's own properties and no others. It panics when the schema does
// not encode, which is a programming error found by any test.
func schema(databases []string, props map[string]*jsonschema.Schema) json.RawMessage {
	db := &jsonschema.Schema{
		Type:        "string",
		Description: "The database to read, by the name Sidekick's settings give it.",
	}

	if len(databases) > 0 {
		db.Enum = make([]any, 0, len(databases))
		for _, name := range databases {
			db.Enum = append(db.Enum, name)
		}
	}

	all := map[string]*jsonschema.Schema{"database": db}
	maps.Copy(all, props)

	s := &jsonschema.Schema{
		Type:                 "object",
		Properties:           all,
		Required:             []string{"database"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(all)),
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("postgres: encode a tool schema: %v", err))
	}

	return data
}

// durationProperty returns a property that holds a number of milliseconds.
func durationProperty(description string) *jsonschema.Schema {
	minimum, maximum := 0.0, float64(maxDurationMs)

	return &jsonschema.Schema{Type: "integer", Description: description, Minimum: &minimum, Maximum: &maximum}
}

// limitProperty returns the limit property of a tool.
func limitProperty() *jsonschema.Schema {
	minimum, maximum := 1.0, float64(maxLimit)

	return &jsonschema.Schema{
		Type:        "integer",
		Description: fmt.Sprintf("The most rows to return, %d to %d. Default %d.", 1, maxLimit, defaultLimit),
		Minimum:     &minimum,
		Maximum:     &maximum,
	}
}

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultLogRange is the time range of query_logs when the call gives no
// since.
const defaultLogRange = time.Hour

// outOfScope matches the KQL functions that read outside the workspace a
// query runs on: other workspaces, Application Insights apps, Azure
// resources, Azure Data Explorer clusters and databases, and Azure
// Resource Graph. The name may be followed by spaces before its
// parenthesis, and KQL names are matched in any case.
var outOfScope = regexp.MustCompile(`(?i)\b(workspace|app|resource|adx|arg|cluster|database)\s*\(`)

// externalData matches the externaldata operator, which makes Log
// Analytics fetch a URL.
var externalData = regexp.MustCompile(`(?i)\bexternaldata\b`)

// logsAnswer is the answer of the Log Analytics query API.
type logsAnswer struct {
	Tables []struct {
		Name    string `json:"name"`
		Columns []struct {
			Name string `json:"name"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"tables"`
	// Error is set when the query ran with a partial result.
	Error *azureError `json:"error"`
}

// logsTable is one table in the result of query_logs.
type logsTable struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// logsResult is the result of query_logs.
type logsResult struct {
	Workspace string      `json:"workspace"`
	Tables    []logsTable `json:"tables"`
	// Warning is the message of a partial result.
	Warning string `json:"warning,omitempty"`
}

// queryLogs runs query_logs. The query goes to Log Analytics as given,
// with the call's time range. The rows are cut to the call's limit.
func (s *Source) queryLogs(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Workspace string `json:"workspace"`
		Query     string `json:"query"`
		Since     string `json:"since"`
		Until     string `json:"until"`
		Limit     int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	ws, err := s.workspace(in.Workspace)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultLogRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if strings.TrimSpace(in.Query) == "" {
		return source.Result{}, &source.ToolError{Message: "query: required"}
	}

	if err := checkScope(in.Query); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	name := workspaceName(ws.ResourceID)
	target := s.apis.logAnalytics + "/workspaces/" + url.PathEscape(ws.ID) + "/query"
	body := map[string]string{"query": in.Query, "timespan": w.timespan()}

	var answer logsAnswer
	if err := s.client.post(ctx, logAnalyticsScope, target, body, &answer); err != nil {
		return source.Result{}, fmt.Errorf("query workspace %s: %w", name, err)
	}

	out, truncated := logRows(answer, limitOf(in.Limit, defaultRows, maxRows))
	out.Workspace = name

	evidence := []source.Evidence{{
		Kind:  source.EvidenceLogQuery,
		Title: "Log Analytics workspace " + name,
		URL:   portal(ws.ResourceID, "/logs"),
		At:    w.end,
	}}

	empty := fmt.Sprintf("The query returned no rows in workspace %s between %s and %s.", name, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, truncated, rowCount(out) == 0, empty)
}

// checkScope refuses a query that could read beyond the one workspace it
// is sent to: a call of a cross-scope function, or the externaldata
// operator. It does not parse KQL, so the name inside a string literal is
// refused too; the model can write the query another way.
func checkScope(query string) error {
	if m := outOfScope.FindStringSubmatch(query); m != nil {
		return &source.ToolError{Message: fmt.Sprintf("query: %s() reads outside the workspace; each call reads one configured "+
			"workspace, so query the tables of that workspace directly", strings.ToLower(m[1]))}
	}

	if externalData.MatchString(query) {
		return &source.ToolError{Message: "query: externaldata fetches data from outside the workspace; each call reads one configured workspace"}
	}

	return nil
}

// logRows returns the tables of an answer with at most limit rows in all,
// each cell cut, and whether anything was cut.
func logRows(answer logsAnswer, limit int) (logsResult, bool) {
	out := logsResult{Tables: make([]logsTable, 0, len(answer.Tables))}
	b := budget{}
	truncated := false
	left := limit

	if answer.Error != nil {
		out.Warning = source.CutText(answer.Error.Message, maxMessageLen)
	}

	for _, t := range answer.Tables {
		table := logsTable{Name: t.Name, Columns: make([]string, 0, len(t.Columns)), Rows: [][]any{}}
		for _, c := range t.Columns {
			table.Columns = append(table.Columns, c.Name)
		}

		for _, row := range t.Rows {
			if left == 0 {
				truncated = true

				break
			}

			cells, cellsCut := cutCells(row)
			if !b.fits(cells) {
				break
			}

			truncated = truncated || cellsCut
			table.Rows = append(table.Rows, cells)
			left--
		}

		out.Tables = append(out.Tables, table)
	}

	return out, truncated || b.cut
}

// cutCells returns a row with each string cell cut to maxCellLen, and
// whether one was.
func cutCells(row []any) ([]any, bool) {
	out := make([]any, len(row))
	cutAny := false

	for i, cell := range row {
		if text, ok := cell.(string); ok {
			var wasCut bool

			cell, wasCut = cut(text, maxCellLen)
			cutAny = cutAny || wasCut
		}

		out[i] = cell
	}

	return out, cutAny
}

// rowCount returns the number of rows in all tables.
func rowCount(r logsResult) int {
	n := 0
	for _, t := range r.Tables {
		n += len(t.Rows)
	}

	return n
}

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of one Logs Insights query.
const (
	// maxQueryLen is the longest query Logs Insights takes.
	maxQueryLen = 10_000
	// maxQueryLogGroups is the most log groups one query reads, the
	// StartQuery limit.
	maxQueryLogGroups = 50
	// maxLogGroupPages is the most pages of log group names one prefix is
	// expanded from.
	maxLogGroupPages = 2
)

// How long query_logs waits for and stops its query.
const (
	// maxQueryWait is how long query_logs waits for a query when the call
	// has no deadline.
	maxQueryWait = 5 * time.Minute
	// stopMargin is the time before the call's deadline at which query_logs
	// stops waiting, so it can stop the query and answer in time.
	stopMargin = 3 * time.Second
	// stopTimeout bounds the StopQuery request.
	stopTimeout = 5 * time.Second
	// waitPrecision is how precisely the error of a stopped query says how
	// long it waited.
	waitPrecision = 10 * time.Millisecond
)

// ptrField is the field Logs Insights adds to each row to find its log
// event again. It means nothing to a reader, so results leave it out.
const ptrField = "@ptr"

// sourceCommand matches the SOURCE command, which picks log groups inside
// the query instead of the log groups the call names: the word source, in
// any case, at the start of any line, such as after a # comment line, or
// after a pipe, followed by anything but a letter, digit or _.
var sourceCommand = regexp.MustCompile(`(?im)(^|\|)\s*source([^A-Za-z0-9_]|$)`)

// logsResult is the result of query_logs.
type logsResult struct {
	Account   string   `json:"account"`
	Region    string   `json:"region"`
	LogGroups []string `json:"logGroups"`
	// LogGroupsCut says more log groups matched the prefixes than one
	// query reads.
	LogGroupsCut bool     `json:"logGroupsCut,omitempty"`
	Fields       []string `json:"fields"`
	// Rows hold one value per field, null where a row has none.
	Rows       [][]*string     `json:"rows"`
	Statistics *logsStatistics `json:"statistics,omitempty"`
}

// logsStatistics is what Logs Insights reports about the scan of a query.
type logsStatistics struct {
	RecordsMatched float64 `json:"recordsMatched"`
	RecordsScanned float64 `json:"recordsScanned"`
	BytesScanned   float64 `json:"bytesScanned"`
}

// queryLogs runs query_logs: it starts the query, polls it until it ends,
// and stops it when the call runs out of time.
func (s *Source) queryLogs(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account   string   `json:"account"`
		Region    string   `json:"region"`
		LogGroups []string `json:"logGroups"`
		Query     string   `json:"query"`
		Since     string   `json:"since"`
		Until     string   `json:"until"`
		Limit     int      `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, 0)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := checkQuery(in.Query); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	wanted, err := s.checkLogGroups(in.LogGroups)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	client := s.logs(t)

	groups, groupsCut, err := s.resolveLogGroups(ctx, client, wanted)
	if err != nil {
		return source.Result{}, fmt.Errorf("find the log groups: %w", err)
	}

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)

	if len(groups) == 0 {
		return source.Result{NoData: true, Text: fmt.Sprintf("No log group in %s matches %s.", where, strings.Join(wanted, ", "))}, nil
	}

	limit := limitOf(in.Limit, defaultRows, maxRows)

	answer, err := s.runQuery(ctx, client, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: groups,
		QueryString:   awssdk.String(in.Query),
		QueryLanguage: types.QueryLanguageCwli,
		StartTime:     awssdk.Int64(w.start.Unix()),
		EndTime:       awssdk.Int64(w.end.Unix()),
		Limit:         awssdk.Int32(int32(limit)), //nolint:gosec // limit is at most maxRows.
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("query the logs of %s: %w", where, err)
	}

	out, truncated := logRows(answer, limit)
	out.Account, out.Region, out.LogGroups, out.LogGroupsCut = t.account.id, t.region, groups, groupsCut

	evidence := []source.Evidence{{
		Kind:  source.EvidenceLogQuery,
		Title: "Logs Insights query in " + where,
		URL:   insightsURL(t.region, groups, in.Query, w),
		At:    w.end,
	}}

	empty := fmt.Sprintf("The query returned no rows from %d log groups in %s between %s and %s.",
		len(groups), where, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, truncated || groupsCut, len(out.Rows) == 0, empty)
}

// checkLogGroups checks the log groups a call names: names, or the start
// of names followed by *, each starting with a prefix the config allows.
// It returns them trimmed and without repeats; prefixes keep their *.
func (s *Source) checkLogGroups(names []string) ([]string, error) {
	if len(s.cfg.LogGroups) == 0 {
		return nil, &source.ToolError{Message: "no log groups are configured for this source, so query_logs reads none"}
	}

	if len(names) == 0 || len(names) > maxInputLogGroups {
		return nil, &source.ToolError{Message: fmt.Sprintf("logGroups: give 1 to %d log groups", maxInputLogGroups)}
	}

	out := make([]string, 0, len(names))

	for _, name := range names {
		name = strings.TrimSpace(name)
		prefix := strings.TrimSuffix(name, "*")

		switch {
		case !logGroupPattern.MatchString(prefix):
			return nil, &source.ToolError{Message: fmt.Sprintf("logGroups: %q is not a log group name, or the start of one followed by *",
				source.CutText(name, maxNameLen))}
		case !s.cfg.allowedLogGroup(prefix):
			return nil, &source.ToolError{Message: fmt.Sprintf("logGroups: %q does not start with a prefix Sidekick may read; allowed: %s",
				name, strings.Join(s.cfg.LogGroups, ", "))}
		}

		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}

	return out, nil
}

// resolveLogGroups returns the log group names of wanted, with each prefix
// replaced by the names that start with it, at most maxQueryLogGroups in
// all, and whether more matched.
func (s *Source) resolveLogGroups(ctx context.Context, client *cloudwatchlogs.Client, wanted []string) ([]string, bool, error) {
	var groups []string

	more := false

	for _, name := range wanted {
		prefix, isPrefix := strings.CutSuffix(name, "*")
		if !isPrefix {
			var full bool

			groups, full = addGroup(groups, name)
			more = more || full

			continue
		}

		var token *string

		for page := 0; page < maxLogGroupPages; page++ {
			out, err := client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
				LogGroupNamePrefix: awssdk.String(prefix),
				NextToken:          token,
			})
			if err != nil {
				return nil, false, fmt.Errorf("list the log groups that start with %s: %w", prefix, err)
			}

			for _, g := range out.LogGroups {
				if found := awssdk.ToString(g.LogGroupName); strings.HasPrefix(found, prefix) && s.cfg.allowedLogGroup(found) {
					var full bool

					groups, full = addGroup(groups, found)
					more = more || full
				}
			}

			token = out.NextToken
			if token == nil {
				break
			}

			if page == maxLogGroupPages-1 {
				more = true
			}
		}
	}

	return groups, more, nil
}

// runQuery starts a query, polls it every pollEvery until it ends, and
// returns its results. When the call is about to run out of time, or a
// poll fails, it stops the query, so it does not keep scanning logs for
// nobody. A poll in flight is let finish: the wait ends between polls,
// before the call's own deadline.
func (s *Source) runQuery(ctx context.Context, client *cloudwatchlogs.Client, in *cloudwatchlogs.StartQueryInput) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	started, err := client.StartQuery(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("start the query: %w", err)
	}

	id := awssdk.ToString(started.QueryId)
	wait := queryWait(ctx)

	stopAt := time.NewTimer(wait)
	defer stopAt.Stop()

	ticker := time.NewTicker(s.pollEvery)
	defer ticker.Stop()

	for {
		out, err := client.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: awssdk.String(id)})

		switch {
		case err != nil && ctx.Err() != nil:
			return nil, stopQuery(ctx, client, id, timedOut(ctx, wait))
		case err != nil:
			return nil, stopQuery(ctx, client, id, fmt.Errorf("read the results of query %s: %w", id, err))
		}

		switch out.Status {
		case types.QueryStatusComplete:
			return out, nil
		case types.QueryStatusFailed, types.QueryStatusCancelled, types.QueryStatusTimeout, types.QueryStatusUnknown:
			return nil, &source.ToolError{Message: fmt.Sprintf("the query ended with status %s", out.Status)}
		}

		select {
		case <-ctx.Done():
			return nil, stopQuery(ctx, client, id, timedOut(ctx, wait))
		case <-stopAt.C:
			return nil, stopQuery(ctx, client, id, timedOut(ctx, wait))
		case <-ticker.C:
		}
	}
}

// queryWait returns how long runQuery may wait for a query: until
// stopMargin before the call's deadline, or half the time left when that
// is shorter.
func queryWait(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return maxQueryWait
	}

	left := time.Until(deadline)

	return max(left-stopMargin, left/2)
}

// timedOut returns the error of a query that did not finish in wait: the
// call's own error when the call was cancelled, otherwise a tool error that
// says how to make the query faster.
func timedOut(ctx context.Context, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wait for the query: %w", err)
	}

	return &source.ToolError{Message: fmt.Sprintf("the query did not finish within %s, so Sidekick stopped it; "+
		"narrow the time range or the log groups, or ask for a longer source timeout", wait.Round(waitPrecision))}
}

// stopQuery stops query id and returns cause, with the failure to stop it
// when there is one. It sends the stop even when the call was cancelled.
func stopQuery(ctx context.Context, client *cloudwatchlogs.Client, id string, cause error) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()

	if _, err := client.StopQuery(stopCtx, &cloudwatchlogs.StopQueryInput{QueryId: awssdk.String(id)}); err != nil {
		return errors.Join(cause, fmt.Errorf("stop query %s: %w", id, err))
	}

	return cause
}

// checkQuery checks the query of a call: not empty, not longer than Logs
// Insights takes, and without a SOURCE command, which would read log
// groups other than the ones the call names.
func checkQuery(query string) error {
	switch {
	case strings.TrimSpace(query) == "":
		return &source.ToolError{Message: "query: required"}
	case len(query) > maxQueryLen:
		return &source.ToolError{Message: fmt.Sprintf("query: %d bytes is longer than the %d Logs Insights takes", len(query), maxQueryLen)}
	case sourceCommand.MatchString(query):
		return &source.ToolError{Message: "query: the SOURCE command picks other log groups; name the log groups in logGroups instead"}
	}

	return nil
}

// addGroup adds name to groups unless it is there already, and reports
// whether name was left out because groups is full.
func addGroup(groups []string, name string) ([]string, bool) {
	switch {
	case slices.Contains(groups, name):
		return groups, false
	case len(groups) == maxQueryLogGroups:
		return groups, true
	}

	return append(groups, name), false
}

// logRows returns the fields and at most limit rows of an answer, with
// each value cut and @ptr left out, and whether anything was cut.
func logRows(answer *cloudwatchlogs.GetQueryResultsOutput, limit int) (logsResult, bool) {
	out := logsResult{Fields: []string{}, Rows: [][]*string{}}

	index := map[string]int{}

	for _, row := range answer.Results {
		for _, f := range row {
			name := awssdk.ToString(f.Field)
			if _, ok := index[name]; !ok && name != ptrField {
				index[name] = len(out.Fields)
				out.Fields = append(out.Fields, name)
			}
		}
	}

	b := budget{}
	truncated := len(answer.Results) > limit

	for _, row := range answer.Results[:min(len(answer.Results), limit)] {
		cells := make([]*string, len(out.Fields))

		for _, f := range row {
			i, ok := index[awssdk.ToString(f.Field)]
			if !ok || f.Value == nil {
				continue
			}

			value, wasCut := cut(*f.Value, maxCellLen)
			truncated = truncated || wasCut
			cells[i] = &value
		}

		if !b.fits(cells) {
			break
		}

		out.Rows = append(out.Rows, cells)
	}

	if st := answer.Statistics; st != nil {
		out.Statistics = &logsStatistics{RecordsMatched: st.RecordsMatched, RecordsScanned: st.RecordsScanned, BytesScanned: st.BytesScanned}

		kept := float64(len(out.Rows))
		truncated = truncated || st.ResultCount > kept || (len(answer.Results) >= limit && st.RecordsMatched > kept)
	}

	return out, truncated || b.cut
}

// insightsURL returns the CloudWatch console link that opens query over
// groups and w in Logs Insights.
func insightsURL(region string, groups []string, query string, w window) string {
	var sources strings.Builder
	for _, g := range groups {
		sources.WriteString("~'" + jsurl(g))
	}

	detail := "~(end~'" + jsurl(consoleTime(w.end)) + "~start~'" + jsurl(consoleTime(w.start)) +
		"~timeType~'ABSOLUTE~tz~'UTC~editorString~'" + jsurl(query) + "~source~(" + sources.String() + "))"

	return consoleURL(region, "cloudwatch", "logsV2:logs-insights$3FqueryDetail$3D"+detail)
}

// consoleTime formats a time the way the CloudWatch console writes it.
func consoleTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

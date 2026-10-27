package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultLogRange is the time range of query_logs when the call gives no
// since.
const defaultLogRange = time.Hour

// maxEntryPages is the most pages of log entries one call reads. Cloud
// Logging can answer a page with few entries, or none, while it searches.
const maxEntryPages = 5

// entriesRequest is the body of entries:list.
type entriesRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter"`
	OrderBy       string   `json:"orderBy"`
	PageSize      int      `json:"pageSize"`
	PageToken     string   `json:"pageToken,omitempty"`
}

// entriesPage is one answer of entries:list.
type entriesPage struct {
	Entries       []logEntry `json:"entries"`
	NextPageToken string     `json:"nextPageToken"`
}

// logEntry is the part of a Cloud Logging entry the source reads.
type logEntry struct {
	LogName      string          `json:"logName"`
	Resource     resource        `json:"resource"`
	Timestamp    time.Time       `json:"timestamp"`
	Severity     string          `json:"severity"`
	TextPayload  string          `json:"textPayload"`
	JSONPayload  json.RawMessage `json:"jsonPayload"`
	ProtoPayload json.RawMessage `json:"protoPayload"`
	HTTPRequest  *httpRequest    `json:"httpRequest"`
	Trace        string          `json:"trace"`
}

// resource is a monitored resource: its type and labels.
type resource struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels,omitempty"`
}

// httpRequest is the request a log entry is about.
type httpRequest struct {
	RequestMethod string `json:"requestMethod,omitempty"`
	RequestURL    string `json:"requestUrl,omitempty"`
	Status        int    `json:"status,omitempty"`
	Latency       string `json:"latency,omitempty"`
}

// logItem is one entry in the result of query_logs.
type logItem struct {
	Time     time.Time    `json:"time"`
	Severity string       `json:"severity,omitempty"`
	Log      string       `json:"log"`
	Resource resource     `json:"resource"`
	Message  string       `json:"message,omitempty"`
	HTTP     *httpRequest `json:"httpRequest,omitempty"`
	Trace    string       `json:"trace,omitempty"`
}

// logsResult is the result of query_logs.
type logsResult struct {
	Project string    `json:"project"`
	Entries []logItem `json:"entries"`
}

// queryLogs runs query_logs. The filter goes to Cloud Logging as given,
// inside the time range of the call.
func (s *Source) queryLogs(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project string `json:"project"`
		Filter  string `json:"filter"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Limit   int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultLogRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter := withTime(in.Filter, w)

	entries, more, err := s.listEntries(ctx, project, filter, limitOf(in.Limit, defaultEntries, maxEntries))
	if err != nil {
		return source.Result{}, fmt.Errorf("search the logs of %s: %w", project, err)
	}

	out := logsResult{Project: project, Entries: make([]logItem, 0, len(entries))}
	b := budget{}
	cut := false

	for _, e := range entries {
		message, messageCut := entryMessage(e)
		labels, labelsCut := cutLabels(e.Resource.Labels)
		item := logItem{
			Time:     e.Timestamp,
			Severity: e.Severity,
			Log:      logID(e.LogName),
			Resource: resource{Type: e.Resource.Type, Labels: labels},
			Message:  message,
			HTTP:     cutRequest(e.HTTPRequest),
			Trace:    e.Trace,
		}

		if !b.fits(item) {
			break
		}

		cut = cut || messageCut || labelsCut
		out.Entries = append(out.Entries, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceLogQuery,
		Title: "Cloud Logging query in " + project,
		URL:   logsExplorer(project, filter, w),
		At:    w.end,
	}}

	empty := fmt.Sprintf("No log entries in %s matched between %s and %s.", project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || cut || b.cut, len(out.Entries) == 0, empty)
}

// listEntries reads up to limit log entries of project that match filter,
// newest first, page by page, reading at most maxEntryPages pages. more is
// true when entries may be left unread.
func (s *Source) listEntries(ctx context.Context, project, filter string, limit int) (entries []logEntry, more bool, err error) {
	req := entriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        filter,
		OrderBy:       "timestamp desc",
	}

	for range maxEntryPages {
		req.PageSize = limit - len(entries)

		var page entriesPage
		if err := s.client.post(ctx, s.apis.logging+"/entries:list", req, &page); err != nil {
			return nil, false, fmt.Errorf("list the entries: %w", err)
		}

		entries = append(entries, page.Entries[:min(len(page.Entries), limit-len(entries))]...)

		if page.NextPageToken == "" {
			return entries, false, nil
		}

		if len(entries) == limit {
			return entries, true, nil
		}

		req.PageToken = page.NextPageToken
	}

	return entries, true, nil
}

// withTime returns filter limited to the time range. The filter goes on
// lines of its own, so a comment at its end cannot hide the range.
func withTime(filter string, w window) string {
	bounds := fmt.Sprintf("timestamp>=%s AND timestamp<%s", quote(timestamp(w.start)), quote(timestamp(w.end)))

	if strings.TrimSpace(filter) == "" {
		return bounds
	}

	return "(\n" + filter + "\n)\nAND " + bounds
}

// entryMessage returns the payload of an entry as text, cut to
// maxMessageLen, and whether it was cut. JSON and proto payloads are kept
// as compact JSON.
func entryMessage(e logEntry) (string, bool) {
	text := e.TextPayload

	switch {
	case len(e.JSONPayload) > 0:
		text = compact(e.JSONPayload)
	case len(e.ProtoPayload) > 0:
		text = compact(e.ProtoPayload)
	}

	text = strings.TrimSpace(text)
	cut := source.CutText(text, maxMessageLen)

	return cut, len(cut) < len(text)
}

// compact returns JSON without its spaces, or as it is when it does not
// parse.
func compact(data json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, data) != nil {
		return string(data)
	}

	return b.String()
}

// cutRequest returns the request of an entry with its URL cut.
func cutRequest(r *httpRequest) *httpRequest {
	if r == nil {
		return nil
	}

	out := *r
	out.RequestURL = source.CutText(out.RequestURL, maxNameLen)

	return &out
}

// logID returns the log ID at the end of a log name, unescaped, such as
// cloudaudit.googleapis.com/activity for
// projects/p/logs/cloudaudit.googleapis.com%2Factivity.
func logID(name string) string {
	_, id, found := strings.Cut(name, "/logs/")
	if !found {
		return name
	}

	if unescaped, err := url.PathUnescape(id); err == nil {
		return unescaped
	}

	return id
}

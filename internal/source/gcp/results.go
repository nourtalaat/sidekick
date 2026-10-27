package gcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// consoleURL is the root of the Google Cloud console.
const consoleURL = "https://console.cloud.google.com"

// Limits on the text a result keeps, in bytes.
const (
	// maxResultSize bounds the JSON of one result. Lists stop adding items
	// once they reach it and say they were cut.
	maxResultSize = 256 << 10
	// maxMessageLen bounds one log message, description or error message.
	maxMessageLen = 2 << 10
	// maxNameLen bounds a name, title or label from Google Cloud.
	maxNameLen = 512
)

// notFoundText is the text of a result for something that does not exist.
const notFoundText = "Not found in Google Cloud: the project or resource does not exist, or Sidekick's identity cannot see it."

// budget counts the bytes a list result holds so far, so a list stops
// before it grows past maxResultSize.
type budget struct {
	used int
	cut  bool
}

// fits adds the size of item to the budget and reports whether it still
// fits. Once one item does not fit, the budget is spent.
func (b *budget) fits(item any) bool {
	if b.cut {
		return false
	}

	data, err := json.Marshal(item)
	if err != nil || b.used+len(data) > maxResultSize {
		b.cut = true

		return false
	}

	b.used += len(data)

	return true
}

// structured returns a result that holds v as JSON, with its evidence.
// When empty says the call found nothing, it returns a result with NoData
// set, the text and the evidence instead, so the link to the empty query
// still reaches the report.
func structured(v any, evidence []source.Evidence, truncated, empty bool, emptyText string) (source.Result, error) {
	if len(evidence) == 0 {
		evidence = nil
	}

	if empty {
		return source.Result{NoData: true, Text: emptyText, Evidence: evidence}, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. Other refusals of the call itself, such as a 400
// for a filter Google Cloud cannot parse or a 403 for a permission the
// identity lacks, are tool errors with Google Cloud's message. A 401, rate
// limits, server errors and token failures are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		switch code := se.code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// console returns a console link to path, for project.
func console(path, project string) string {
	return consoleURL + path + "?project=" + url.QueryEscape(project)
}

// logsExplorer returns the Logs Explorer link of a filter in a time range.
func logsExplorer(project, filter string, w window) string {
	return console("/logs/query;query="+escape(filter)+";timeRange="+escape(timestamp(w.start)+"/"+timestamp(w.end)), project)
}

// escape escapes s for one parameter of a console path, where spaces must
// be %20 and ';', '=' and '/' must be escaped too.
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// cutLabels returns labels with each value cut to maxNameLen, and reports
// whether one was cut.
func cutLabels(labels map[string]string) (map[string]string, bool) {
	cut := false

	for k, v := range labels {
		if len(v) > maxNameLen {
			labels[k] = source.CutText(v, maxNameLen)
			cut = true
		}
	}

	return labels, cut
}

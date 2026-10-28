package elasticsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the text a result keeps, in bytes.
const (
	// maxResultSize bounds the JSON of one result. Lists stop adding items
	// once they reach it and say they were cut.
	maxResultSize = 256 << 10
	// maxMessageLen bounds one field value of a hit, and an error message.
	maxMessageLen = 2 << 10
	// maxNameLen bounds a name or a type from the server.
	maxNameLen = 512
	// maxTitleQueryLen bounds the query shown in an evidence title.
	maxTitleQueryLen = 200
)

// budget counts the bytes a list result holds so far, so a list stops
// before it grows past maxResultSize.
type budget struct {
	used int
	cut  bool
}

// linkData is the data of the discoverURL template. Each field is escaped
// for a URL and for a quoted Rison string, the form Kibana and OpenSearch
// Dashboards keep their state in.
type linkData struct {
	Index string
	Query string
	From  time.Time
	To    time.Time
}

// escapedLink is linkData with every field escaped, as the template sees
// it.
type escapedLink struct {
	Index string
	Query string
	From  string
	To    string
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

// evidence returns the evidence of a search of index with query in w: a
// link from the discoverURL template, or, without one, a title that says
// what was searched.
func (s *Source) evidence(index, query string, w window) []source.Evidence {
	title := fmt.Sprintf("%s search of %s", s.cfg.product(), index)
	if query != "" {
		title += fmt.Sprintf(" for %q", source.CutText(query, maxTitleQueryLen))
	}

	title += " from " + timestamp(w.start) + " to " + timestamp(w.end)

	e := source.Evidence{Kind: source.EvidenceLogQuery, Title: title, At: w.end}

	if s.discover != nil {
		if link, err := renderLink(s.discover, linkData{Index: index, Query: query, From: w.start, To: w.end}); err == nil {
			e.URL = link
		}
	}

	return []source.Evidence{e}
}

// structured returns a result that holds v as JSON, with its evidence.
// When empty says the call found nothing, it returns a result with NoData
// set, the text and the evidence instead, so the link to the empty search
// still reaches the report.
func structured(v any, evidence []source.Evidence, truncated, empty bool, emptyText string) (source.Result, error) {
	if empty {
		return source.Result{NoData: true, Text: emptyText, Evidence: evidence}, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// failure turns the error of a call into what Call returns. A 404, such as
// an index that does not exist, is a result with NoData set. Other refusals
// of the call itself, such as a 400 for a query the server cannot parse or
// a 403 for a privilege the role lacks, are tool errors with the server's
// message. A 401, rate limits, server errors and secrets that cannot be
// read are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		switch code := se.code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: "Not found: " + se.Error()}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// renderLink fills the discoverURL template with d, each field escaped.
func renderLink(tmpl *template.Template, d linkData) (string, error) {
	escaped := escapedLink{
		Index: escapeRison(d.Index),
		Query: escapeRison(d.Query),
		From:  escapeRison(timestamp(d.From)),
		To:    escapeRison(timestamp(d.To)),
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, escaped); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}

	return b.String(), nil
}

// escapeRison escapes s for a quoted Rison string, where an exclamation mark
// and a single quote are escaped with an exclamation mark, and then for a
// URL, with spaces as %20.
func escapeRison(s string) string {
	s = strings.NewReplacer("!", "!!", "'", "!'").Replace(s)

	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

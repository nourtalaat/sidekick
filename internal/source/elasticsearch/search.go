package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// searchRequest is the body of a _search request. The source builds every
// body itself; a call never passes one.
type searchRequest struct {
	Size           int              `json:"size"`
	Query          boolQuery        `json:"query"`
	Sort           []map[string]any `json:"sort,omitempty"`
	Source         *sourceFilter    `json:"_source,omitempty"`
	TrackTotalHits bool             `json:"track_total_hits"`
	Aggs           map[string]any   `json:"aggs,omitempty"`
}

// boolQuery is the query of a search: the time range and, when the call
// gives one, the query string, both as filters.
type boolQuery struct {
	Bool struct {
		Filter []map[string]any `json:"filter"`
	} `json:"bool"`
}

// sourceFilter lists the fields of _source a search returns.
type sourceFilter struct {
	Includes []string `json:"includes"`
}

// searchAnswer is the part of a _search answer the source reads.
type searchAnswer struct {
	TimedOut bool `json:"timed_out"`
	Shards   struct {
		Failed int `json:"failed"`
	} `json:"_shards"`
	Hits struct {
		Total struct {
			Value    int    `json:"value"`
			Relation string `json:"relation"`
		} `json:"total"`
		Hits []struct {
			Index  string         `json:"_index"`
			ID     string         `json:"_id"`
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
	Aggregations json.RawMessage `json:"aggregations"`
}

// hitItem is one hit in the result of search_logs.
type hitItem struct {
	Index  string         `json:"index"`
	ID     string         `json:"id"`
	Fields map[string]any `json:"fields"`
}

// searchResult is the result of search_logs.
type searchResult struct {
	Index string `json:"index"`
	// Total is the number of matching logs, or a lower bound of it when
	// TotalAtLeast is set.
	Total        int       `json:"total"`
	TotalAtLeast bool      `json:"totalAtLeast,omitempty"`
	Hits         []hitItem `json:"hits"`
	// Partial says that some shards failed or the search timed out, so
	// logs may be missing.
	Partial bool `json:"partial,omitempty"`
}

// searchLogs runs search_logs: the newest hits of the query string in the
// time range, with only the chosen fields of each.
func (s *Source) searchLogs(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Index  string   `json:"index"`
		Query  string   `json:"query"`
		Since  string   `json:"since"`
		Until  string   `json:"until"`
		Fields []string `json:"fields"`
		Limit  int      `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	q, err := s.checkSearch(in.Index, in.Query, in.Since, in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	fields, err := s.fields(in.Fields)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	req := searchRequest{
		Size:           limitOf(in.Limit, defaultHits, maxHits),
		Query:          s.filter(q),
		Sort:           []map[string]any{{s.cfg.timeField(): map[string]string{"order": "desc", "unmapped_type": "date"}}},
		Source:         &sourceFilter{Includes: fields},
		TrackTotalHits: true,
	}

	var answer searchAnswer
	if err := s.client.search(ctx, q.index, req, &answer); err != nil {
		return source.Result{}, fmt.Errorf("search %s: %w", q.index, err)
	}

	out := searchResult{
		Index:        q.index,
		Total:        answer.Hits.Total.Value,
		TotalAtLeast: answer.Hits.Total.Relation == "gte",
		Hits:         make([]hitItem, 0, len(answer.Hits.Hits)),
		Partial:      answer.TimedOut || answer.Shards.Failed > 0,
	}

	b := budget{}
	cut := false

	for _, h := range answer.Hits.Hits {
		values, valuesCut := flatten(h.Source)
		item := hitItem{Index: source.CutText(h.Index, maxNameLen), ID: source.CutText(h.ID, maxNameLen), Fields: values}

		if !b.fits(item) {
			break
		}

		cut = cut || valuesCut
		out.Hits = append(out.Hits, item)
	}

	more := out.TotalAtLeast || out.Total > len(out.Hits)
	empty := fmt.Sprintf("No logs in %s matched between %s and %s.", q.index, timestamp(q.w.start), timestamp(q.w.end))

	return structured(out, s.evidence(q.index, q.query, q.w), more || cut || b.cut || out.Partial, len(out.Hits) == 0, empty)
}

// checked is the checked input that search_logs and count_logs share.
type checked struct {
	index string
	query string
	w     window
}

// checkSearch checks the index, query string and time range of a call.
func (s *Source) checkSearch(index, query, since, until string) (checked, error) {
	idx, err := s.index(index)
	if err != nil {
		return checked{}, fmt.Errorf("index: %w", err)
	}

	q, err := s.query(query)
	if err != nil {
		return checked{}, fmt.Errorf("query: %w", err)
	}

	w, err := s.window(since, until)
	if err != nil {
		return checked{}, fmt.Errorf("time range: %w", err)
	}

	return checked{index: idx, query: q, w: w}, nil
}

// filter returns the query of a search: the time range on the time field
// and, when given, the query string.
func (s *Source) filter(c checked) boolQuery {
	var q boolQuery

	q.Bool.Filter = []map[string]any{{"range": map[string]any{s.cfg.timeField(): map[string]string{
		"gte":    timestamp(c.w.start),
		"lt":     timestamp(c.w.end),
		"format": "strict_date_optional_time",
	}}}}

	if c.query != "" {
		q.Bool.Filter = append(q.Bool.Filter, map[string]any{"query_string": map[string]string{"query": c.query}})
	}

	return q
}

// flatten returns the fields of a document's _source with dotted names,
// such as service.name for {"service": {"name": ...}}, every string value
// cut to maxMessageLen, and whether one was cut. Arrays keep their values,
// each cut the same way.
func flatten(doc map[string]any) (map[string]any, bool) {
	out := map[string]any{}
	cut := false

	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}

			if inner, ok := v.(map[string]any); ok {
				walk(name, inner)

				continue
			}

			value, valueCut := cutValue(v)
			cut = cut || valueCut
			out[name] = value
		}
	}

	walk("", doc)

	return out, cut
}

// cutValue returns v with every string in it cut to maxMessageLen, without
// the spaces around it, and whether one was cut.
func cutValue(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		text := strings.TrimSpace(t)
		short := source.CutText(text, maxMessageLen)

		return short, len(short) < len(text)
	case []any:
		out := make([]any, len(t))
		cut := false

		for i, item := range t {
			var itemCut bool
			out[i], itemCut = cutValue(item)
			cut = cut || itemCut
		}

		return out, cut
	case map[string]any:
		return flatten(t)
	default:
		return v, false
	}
}

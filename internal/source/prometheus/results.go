package prometheus

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/promql"
)

// Limits on the text a result keeps, in bytes.
const (
	// maxResultSize bounds the JSON of one result. Lists stop adding items
	// once they reach it and say they were cut.
	maxResultSize = 256 << 10
	// maxMessageLen bounds one annotation, warning or error message.
	maxMessageLen = 2 << 10
	// maxNameLen bounds a name, label value or string value.
	maxNameLen = 512
)

// notFoundText is the text of a result for an API the server does not
// have.
const notFoundText = "Not found: the server does not have this API, or baseURL points at the wrong path."

// promUITime is the time format of the Prometheus web UI's URL parameters,
// in UTC.
const promUITime = "2006-01-02 15:04:05"

// budget counts the bytes a list result holds so far, so a list stops
// before it grows past maxResultSize.
type budget struct {
	used int
	cut  bool
}

// point is one sample in a result: [<unix seconds>, <value>]. The value is
// a number, or a string such as "NaN" or "+Inf" that JSON cannot hold as a
// number.
type point [2]any

// stats sums up the values of a series that are numbers.
type stats struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Avg  float64 `json:"avg"`
	Last float64 `json:"last"`
}

// links makes the evidence links of queries: to Grafana Explore when a
// Grafana is set, otherwise to the Prometheus web UI.
type links struct {
	prometheus string
	grafana    string
	datasource string
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

// query returns the evidence of a query over w, as a graph, or as a table
// at w.end when instant is set.
func (l links) query(title, expr string, w window, instant bool) source.Evidence {
	if l.grafana != "" {
		return source.Evidence{Kind: source.EvidenceMetric, Title: title + " in Grafana Explore", URL: l.explore(expr, w, instant), At: w.end}
	}

	params := url.Values{"g0.expr": {expr}}

	if instant {
		params.Set("g0.tab", "1")
		params.Set("g0.moment_input", w.end.UTC().Format(promUITime))
	} else {
		params.Set("g0.tab", "0")
		params.Set("g0.range_input", uiDuration(w.end.Sub(w.start)))
		params.Set("g0.end_input", w.end.UTC().Format(promUITime))
	}

	return source.Evidence{Kind: source.EvidenceMetric, Title: title + " in Prometheus", URL: l.prometheus + "/graph?" + params.Encode(), At: w.end}
}

// explore returns the Grafana Explore link of a query over w.
func (l links) explore(expr string, w window, instant bool) string {
	type datasource struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}

	type query struct {
		RefID      string     `json:"refId"`
		Expr       string     `json:"expr"`
		Range      bool       `json:"range"`
		Instant    bool       `json:"instant"`
		Datasource datasource `json:"datasource"`
	}

	type pane struct {
		Datasource string  `json:"datasource"`
		Queries    []query `json:"queries"`
		Range      struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"range"`
	}

	p := pane{
		Datasource: l.datasource,
		Queries: []query{{
			RefID: "A", Expr: expr, Range: !instant, Instant: instant,
			Datasource: datasource{Type: "prometheus", UID: l.datasource},
		}},
	}

	p.Range.From = strconv.FormatInt(w.start.UnixMilli(), 10)
	p.Range.To = strconv.FormatInt(w.end.UnixMilli(), 10)

	data, err := json.Marshal(map[string]pane{"sidekick": p})
	if err != nil {
		return l.grafana + "/explore"
	}

	return l.grafana + "/explore?" + url.Values{"schemaVersion": {"1"}, "panes": {string(data)}}.Encode()
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
// result with NoData set. An answer too large to read, and other refusals
// of the call itself, such as a 400 for a query the server cannot run or
// a 422 for one that failed while it ran, are tool errors with the
// server's message. A 401, rate limits, server errors and secrets that
// cannot be read are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if errors.Is(err, errResponseTooLarge) {
		return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: errResponseTooLarge.Error()})
	}

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

// answerError returns the *source.ToolError of an answer whose status is
// not success.
func answerError[T any](answer promql.Answer[T]) error {
	text := answer.Error
	if answer.ErrorType != "" {
		text = answer.ErrorType + ": " + text
	}

	return &source.ToolError{Message: "the server answered with an error: " + source.CutText(text, maxMessageLen)}
}

// newPoint returns the point of a sample.
func newPoint(s promql.Sample) point {
	return point{unixSeconds(s.T), sampleValue(s.V)}
}

// statsOf returns the stats of the samples whose values are numbers, or
// nil when none is.
func statsOf(samples []promql.Sample) *stats {
	var (
		st    stats
		sum   float64
		count int
	)

	for _, s := range samples {
		f, err := strconv.ParseFloat(s.V, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}

		if count == 0 || f < st.Min {
			st.Min = f
		}

		if count == 0 || f > st.Max {
			st.Max = f
		}

		sum += f
		count++
		st.Last = f
	}

	if count == 0 {
		return nil
	}

	st.Avg = sum / float64(count)

	return &st
}

// sampleValue returns a sample value as a number, or as the text
// Prometheus wrote when JSON cannot hold it as one.
func sampleValue(v string) any {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return source.CutText(v, maxNameLen)
	}

	return f
}

// unixSeconds returns t in Unix seconds, with the milliseconds Prometheus
// keeps.
func unixSeconds(t time.Time) json.Number {
	return json.Number(strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', -1, 64))
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

// cutTexts returns texts with each one cut to maxMessageLen.
func cutTexts(texts []string) []string {
	if len(texts) == 0 {
		return nil
	}

	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = source.CutText(t, maxMessageLen)
	}

	return out
}

// uiDuration formats d the way the Prometheus web UI takes a range, such
// as 1d2h or 90s.
func uiDuration(d time.Duration) string {
	d = max(d.Round(time.Second), time.Second)
	out := ""

	for _, unit := range []struct {
		name string
		size time.Duration
	}{{"d", 24 * time.Hour}, {"h", time.Hour}, {"m", time.Minute}, {"s", time.Second}} {
		if n := d / unit.size; n > 0 {
			out += strconv.FormatInt(int64(n), 10) + unit.name
			d -= n * unit.size
		}
	}

	return out
}

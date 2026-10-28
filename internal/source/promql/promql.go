// Package promql reads the answers of the Prometheus HTTP API, which
// Prometheus and the services compatible with it share, such as Cloud
// Monitoring, Thanos and Mimir. The sources that run PromQL use it, so they
// read the answers the same way.
package promql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// Result types of a query answer.
const (
	TypeMatrix = "matrix"
	TypeVector = "vector"
	TypeScalar = "scalar"
	TypeString = "string"
)

// msPerSecond turns the Unix seconds of a sample into the milliseconds
// Prometheus keeps.
const msPerSecond = 1000

// StatusSuccess is the status of an answer that holds data.
const StatusSuccess = "success"

// errNotSeries is returned by Series for a result that holds no series.
var errNotSeries = errors.New("the result holds no series")

// errNotSample is returned by Sample for a result that is not one sample.
var errNotSample = errors.New("the result is not a scalar or a string")

// Answer is the JSON envelope of every Prometheus HTTP API answer, with the
// data of the endpoint in Data.
type Answer[T any] struct {
	Status    string   `json:"status"`
	ErrorType string   `json:"errorType"`
	Error     string   `json:"error"`
	Data      T        `json:"data"`
	Warnings  []string `json:"warnings"`
	Infos     []string `json:"infos"`
}

// QueryData is the data of an answer of the query and query_range
// endpoints. Result is read with Series or Sample, by ResultType.
type QueryData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// Series is one series of a matrix or vector result: a matrix series has
// Values, a vector series has Value. A series of native histograms has
// neither.
type Series struct {
	Metric map[string]string `json:"metric"`
	Value  *Sample           `json:"value"`
	Values []Sample          `json:"values"`
}

// Sample is one sample: its time, and its value as Prometheus writes it,
// such as "0.25", "NaN" or "+Inf".
type Sample struct {
	T time.Time
	V string
}

// Series returns the series of a matrix or vector result. An answer with
// no result holds no series.
func (d QueryData) Series() ([]Series, error) {
	if empty(d.Result) {
		return nil, nil
	}

	if d.ResultType != TypeMatrix && d.ResultType != TypeVector {
		return nil, fmt.Errorf("result type %q: %w", d.ResultType, errNotSeries)
	}

	var series []Series

	if err := decodeResult(d.Result, &series); err != nil {
		return nil, fmt.Errorf("read the %s result: %w", d.ResultType, err)
	}

	return series, nil
}

// Sample returns the one sample of a scalar or string result.
func (d QueryData) Sample() (Sample, error) {
	if d.ResultType != TypeScalar && d.ResultType != TypeString {
		return Sample{}, fmt.Errorf("result type %q: %w", d.ResultType, errNotSample)
	}

	var s Sample

	if err := decodeResult(d.Result, &s); err != nil {
		return Sample{}, fmt.Errorf("read the %s result: %w", d.ResultType, err)
	}

	return s, nil
}

// UnmarshalJSON reads a sample in the form Prometheus writes it:
// [<unix seconds>, "<value>"]. The time is rounded to the millisecond,
// the precision Prometheus keeps, so a time such as 1777885200.123 is not
// read as 1777885200.122999906.
func (s *Sample) UnmarshalJSON(data []byte) error {
	var pair [2]any

	if err := json.Unmarshal(data, &pair); err != nil {
		return fmt.Errorf("decode a sample: %w", err)
	}

	if secs, ok := pair[0].(float64); ok {
		s.T = time.UnixMilli(int64(math.Round(secs * msPerSecond))).UTC()
	}

	if v, ok := pair[1].(string); ok {
		s.V = v
	}

	return nil
}

// decodeResult decodes a result, where a missing or null result is empty.
func decodeResult(data json.RawMessage, out any) error {
	if empty(data) {
		return nil
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode the result: %w", err)
	}

	return nil
}

// empty reports whether a result is missing or null.
func empty(data json.RawMessage) bool {
	trimmed := bytes.TrimSpace(data)

	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

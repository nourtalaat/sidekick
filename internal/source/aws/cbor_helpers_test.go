package aws

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/smithy-go/encoding/cbor"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// CloudWatch and AWS Health speak Smithy RPC v2 CBOR. These helpers let the
// tests write their answers and check their requests as JSON.

// timeKey marks a timestamp in the JSON that cborAnswer turns into CBOR:
// {"$time": <epoch seconds>}.
const timeKey = "$time"

// cborAnswer returns a CBOR answer of CloudWatch or AWS Health, from JSON in
// which {"$time": n} stands for the timestamp of epoch second n.
func cborAnswer(t *testing.T, status int, body string) fakehttp.Response {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()

	var v any
	require.NoError(t, dec.Decode(&v))

	return fakehttp.Response{
		Status:  status,
		Headers: map[string]string{"Content-Type": "application/cbor", "Smithy-Protocol": "rpc-v2-cbor"},
		Body:    cbor.Encode(toCBOR(t, v)),
	}
}

// cborRefused returns a CBOR error answer of CloudWatch or AWS Health.
func cborRefused(t *testing.T, status int, code, message string) fakehttp.Response {
	t.Helper()

	return cborAnswer(t, status, fmt.Sprintf(`{"__type":%q,"message":%q}`, code, message))
}

// cborJSON returns the CBOR body of a request as JSON, with timestamps as
// epoch seconds.
func cborJSON(t *testing.T, body []byte) string {
	t.Helper()

	v, err := cbor.Decode(body)
	require.NoError(t, err)

	data, err := json.Marshal(fromCBOR(v))
	require.NoError(t, err)

	return string(data)
}

// toCBOR converts a decoded JSON value to CBOR.
func toCBOR(t *testing.T, v any) cbor.Value {
	t.Helper()

	switch v := v.(type) {
	case map[string]any:
		if n, ok := v[timeKey].(json.Number); ok && len(v) == 1 {
			f, err := n.Float64()
			require.NoError(t, err)

			return &cbor.Tag{ID: 1, Value: cbor.Float64(f)}
		}

		m := cbor.Map{}
		for k, item := range v {
			m[k] = toCBOR(t, item)
		}

		return m
	case []any:
		l := cbor.List{}
		for _, item := range v {
			l = append(l, toCBOR(t, item))
		}

		return l
	case string:
		return cbor.String(v)
	case bool:
		return cbor.Bool(v)
	case json.Number:
		return cborNumber(t, v)
	default:
		return &cbor.Nil{}
	}
}

// cborNumber converts a JSON number to a CBOR integer or float.
func cborNumber(t *testing.T, n json.Number) cbor.Value {
	t.Helper()

	if i, err := n.Int64(); err == nil {
		if i < 0 {
			return cbor.NegInt(uint64(-i)) //nolint:gosec // -i is above zero here.
		}

		return cbor.Uint(uint64(i)) //nolint:gosec // i is not negative here.
	}

	f, err := n.Float64()
	require.NoError(t, err)

	return cbor.Float64(f)
}

// fromCBOR converts a CBOR value to one json.Marshal takes.
func fromCBOR(v cbor.Value) any {
	switch v := v.(type) {
	case cbor.Map:
		m := map[string]any{}
		for k, item := range v {
			m[k] = fromCBOR(item)
		}

		return m
	case cbor.List:
		l := []any{}
		for _, item := range v {
			l = append(l, fromCBOR(item))
		}

		return l
	case cbor.String:
		return string(v)
	case cbor.Bool:
		return bool(v)
	case cbor.Uint:
		return uint64(v)
	case cbor.NegInt:
		return -float64(v)
	case cbor.Float32:
		return float64(v)
	case cbor.Float64:
		return float64(v)
	case *cbor.Tag:
		return fromCBOR(v.Value)
	default:
		return nil
	}
}

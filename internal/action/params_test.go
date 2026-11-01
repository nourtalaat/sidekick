package action

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalParams(t *testing.T) {
	t.Parallel()

	nested := strings.Repeat(`{"a":`, maxParamsDepth+1) + "1" + strings.Repeat("}", maxParamsDepth+1)

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "keys are sorted and spaces dropped", raw: ` { "b": 1,  "a": [true, null, "x"] } `, want: `{"a":[true,null,"x"],"b":1}`},
		{name: "numbers keep their text", raw: `{"n": 12345678901234567890, "f": 1.50}`, want: `{"f":1.50,"n":12345678901234567890}`},
		{name: "html characters are not escaped", raw: `{"t": "<b>&</b>"}`, want: `{"t":"<b>&</b>"}`},
		{name: "an empty object", raw: `{}`, want: `{}`},
		{name: "a key twice", raw: `{"a": 1, "a": 2}`, wantErr: `the key "a" is given twice`},
		{name: "a key twice below the top", raw: `{"a": [{"b": 1, "b": 2}]}`, wantErr: `the key "b" is given twice`},
		{name: "the same key in two objects", raw: `{"a": {"k": 1}, "b": {"k": 2}}`, want: `{"a":{"k":1},"b":{"k":2}}`},
		{name: "an array", raw: `[1]`, wantErr: "must be a JSON object"},
		{name: "a string", raw: `"x"`, wantErr: "must be a JSON object"},
		{name: "empty", raw: ``, wantErr: "not valid JSON"},
		{name: "text after the object", raw: `{} {}`, wantErr: "not valid JSON"},
		{name: "invalid UTF-8", raw: "{\"a\": \"\xff\"}", wantErr: "not valid JSON"},
		{name: "too deep", raw: nested, wantErr: "levels deep"},
		{name: "too long", raw: `{"a":"` + strings.Repeat("x", MaxParamsBytes) + `"}`, wantErr: "at most"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalParams(json.RawMessage(tt.raw))
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrParams)
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestDisplayParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
		want   string
	}{
		{name: "indented", params: `{"b":1,"a":"x"}`, want: "{\n  \"a\": \"x\",\n  \"b\": 1\n}"},
		{name: "a newline in a value shows as an escape", params: `{"a":"x\ny"}`, want: "{\n  \"a\": \"x\\ny\"\n}"},
		{name: "a backtick cannot close a code block", params: "{\"a\":\"```\"}", want: "{\n  \"a\": \"\\u0060\\u0060\\u0060\"\n}"},
		{name: "a right-to-left override", params: "{\"a\":\"ab‮cd\"}", want: "{\n  \"a\": \"ab\\u202ecd\"\n}"},
		{name: "a zero-width space and a no-break space", params: "{\"a\":\"a​b c\"}", want: "{\n  \"a\": \"a\\u200bb\\u00a0c\"\n}"},
		{name: "a delete character", params: "{\"a\":\"a\u007fb\"}", want: "{\n  \"a\": \"a\\u007fb\"\n}"},
		{name: "a tag character above the basic plane", params: "{\"a\":\"a\U000E0041b\"}", want: "{\n  \"a\": \"a\\udb40\\udc41b\"\n}"},
		{name: "text in other scripts is kept", params: `{"a":"Grüße 日本"}`, want: "{\n  \"a\": \"Grüße 日本\"\n}"},
		{name: "html characters are kept", params: `{"a":"<b>&"}`, want: "{\n  \"a\": \"<b>&\"\n}"},
		{name: "text that is not JSON is spelled out", params: "oops\u202e`", want: "oops\\u202e\\u0060"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, DisplayParams(json.RawMessage(tt.params)))
		})
	}
}

package action

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    Path
		wantErr bool
	}{
		{in: "message", want: Path{"message"}},
		{in: "options.thresholds.critical", want: Path{"options", "thresholds", "critical"}},
		{in: "data[0].model.expr", want: Path{"data", "[0]", "model", "expr"}},
		{in: "data[*].model", want: Path{"data", "[*]", "model"}},
		{in: "a[0][1]", want: Path{"a", "[0]", "[1]"}},
		{in: "", wantErr: true},
		{in: ".a", wantErr: true},
		{in: "a..b", wantErr: true},
		{in: "a.", wantErr: true},
		{in: "a[", wantErr: true},
		{in: "a]", wantErr: true},
		{in: "a[x]", wantErr: true},
		{in: "a[-1]", wantErr: true},
		{in: "a[01]", wantErr: true},
		{in: "a[0]b", wantErr: true},
		{in: "[0]", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePath(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrPath)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.in, got.String())
		})
	}
}

func TestPathWithin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path, allows string
		want         bool
	}{
		{name: "same", path: "message", allows: "message", want: true},
		{name: "below", path: "options.thresholds.critical", allows: "options.thresholds", want: true},
		{name: "above", path: "options.thresholds", allows: "options.thresholds.critical"},
		{name: "sibling", path: "options.thresholds.warning", allows: "options.thresholds.critical"},
		{name: "look-alike prefix", path: "messages", allows: "message"},
		{name: "any position", path: "data[3].model.expr", allows: "data[*].model", want: true},
		{name: "one position", path: "data[3].model", allows: "data[2]"},
		{name: "position is not a key", path: "data.x", allows: "data[*]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := ParsePath(tt.path)
			require.NoError(t, err)

			a, err := ParsePath(tt.allows)
			require.NoError(t, err)

			require.Equal(t, tt.want, p.Within(a))
		})
	}

	require.False(t, Path{"a"}.Within(nil))
	require.False(t, Path(nil).Within(Path{"a"}))
}

func TestCheckFields(t *testing.T) {
	t.Parallel()

	allowed, err := ParsePaths([]string{"options.thresholds", "message"})
	require.NoError(t, err)

	deltas, err := Diff(
		json.RawMessage(`{"message":"a","options":{"thresholds":{"critical":1},"notify_no_data":false}}`),
		json.RawMessage(`{"message":"b","options":{"thresholds":{"critical":2},"notify_no_data":false}}`))
	require.NoError(t, err)
	require.NoError(t, CheckFields(allowed, deltas))

	deltas, err = Diff(
		json.RawMessage(`{"message":"a","options":{"notify_no_data":false}}`),
		json.RawMessage(`{"message":"a","options":{"notify_no_data":true}}`))
	require.NoError(t, err)
	require.ErrorIs(t, CheckFields(allowed, deltas), ErrLimit)

	_, err = ParsePaths([]string{"ok", "a["})
	require.ErrorIs(t, err, ErrPath)
}

func TestApply(t *testing.T) {
	t.Parallel()

	doc := `{"message":"a","options":{"thresholds":{"critical":1}},"rules":[{"expr":"x"}],"tags":["t"]}`

	tests := []struct {
		name    string
		edits   []Edit
		want    string
		wantErr bool
	}{
		{name: "set a nested value", edits: []Edit{{Path: Path{"options", "thresholds", "critical"}, Value: json.RawMessage(`9`)}}, want: `{"message":"a","options":{"thresholds":{"critical":9}},"rules":[{"expr":"x"}],"tags":["t"]}`},
		{name: "make missing objects", edits: []Edit{{Path: Path{"options", "extra", "k"}, Value: json.RawMessage(`true`)}}, want: `{"message":"a","options":{"extra":{"k":true},"thresholds":{"critical":1}},"rules":[{"expr":"x"}],"tags":["t"]}`},
		{name: "set in an array", edits: []Edit{{Path: Path{"rules", "[0]", "expr"}, Value: json.RawMessage(`"y"`)}}, want: `{"message":"a","options":{"thresholds":{"critical":1}},"rules":[{"expr":"y"}],"tags":["t"]}`},
		{name: "remove a key", edits: []Edit{{Path: Path{"options"}}}, want: `{"message":"a","rules":[{"expr":"x"}],"tags":["t"]}`},
		{name: "remove an absent key", edits: []Edit{{Path: Path{"options", "nothing", "here"}}}, want: `{"message":"a","options":{"thresholds":{"critical":1}},"rules":[{"expr":"x"}],"tags":["t"]}`},
		{name: "position past the end", edits: []Edit{{Path: Path{"rules", "[3]", "expr"}, Value: json.RawMessage(`1`)}}, wantErr: true},
		{name: "into a string", edits: []Edit{{Path: Path{"message", "x"}, Value: json.RawMessage(`1`)}}, wantErr: true},
		{name: "remove an element", edits: []Edit{{Path: Path{"tags", "[0]"}}}, wantErr: true},
		{name: "remove everything", edits: []Edit{{Path: nil}}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Apply(json.RawMessage(doc), tt.edits)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestRevert(t *testing.T) {
	t.Parallel()

	before := json.RawMessage(`{"message":"old","options":{"thresholds":{"critical":80}},"tags":["a"]}`)
	after := json.RawMessage(`{"message":"new","options":{"thresholds":{"critical":90},"renotify_interval":30},"tags":["a"]}`)

	deltas, err := Diff(before, after)
	require.NoError(t, err)

	// Someone else changed the tags after Sidekick's change; a revert leaves that alone.
	live := json.RawMessage(`{"message":"new","options":{"thresholds":{"critical":90},"renotify_interval":30},"tags":["a","b"]}`)

	got, err := Revert(live, deltas)
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"old","options":{"thresholds":{"critical":80}},"tags":["a","b"]}`, string(got))
}

func TestAtAfterAndAtBefore(t *testing.T) {
	t.Parallel()

	before := json.RawMessage(`{"message":"old","options":{"critical":80}}`)
	after := json.RawMessage(`{"message":"new","options":{"critical":90,"renotify":30}}`)

	deltas, err := Diff(before, after)
	require.NoError(t, err)

	tests := []struct {
		name       string
		live       string
		wantAfter  bool
		wantBefore bool
	}{
		{name: "as Sidekick wrote it", live: `{"message":"new","options":{"critical":90.0,"renotify":30},"tags":["x"]}`, wantAfter: true},
		{name: "as it was", live: `{"message":"old","options":{"critical":80},"other":1}`, wantBefore: true},
		{name: "someone changed a field", live: `{"message":"new","options":{"critical":95,"renotify":30}}`},
		{name: "a field is gone", live: `{"message":"new","options":{"critical":90}}`},
		{name: "options is not an object", live: `{"message":"new","options":3}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotAfter, err := AtAfter(json.RawMessage(tt.live), deltas)
			require.NoError(t, err)
			require.Equal(t, tt.wantAfter, gotAfter)

			gotBefore, err := AtBefore(json.RawMessage(tt.live), deltas)
			require.NoError(t, err)
			require.Equal(t, tt.wantBefore, gotBefore)
		})
	}

	_, err = AtAfter(json.RawMessage(`{`), deltas)
	require.Error(t, err)
}

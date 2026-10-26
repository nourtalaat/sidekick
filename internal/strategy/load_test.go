package strategy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/strategies"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		builtIn fs.FS
		dir     string
		// want maps each strategy name to its expected origin.
		want    map[string]Origin
		wantLog string
		check   func(t *testing.T, set *Set)
	}{
		{
			name: "built-in strategies only",
			want: map[string]Origin{GenericAlert: BuiltIn},
			check: func(t *testing.T, set *Set) {
				t.Helper()

				st, _ := set.Get(GenericAlert)
				require.Equal(t, "generic-alert.yaml", st.Path)
				require.NotEmpty(t, st.First)
				require.Len(t, st.Version, versionLength)
			},
		},
		{
			name: "a dir that does not exist adds nothing",
			dir:  filepath.Join("fixture", "missing"),
			want: map[string]Origin{GenericAlert: BuiltIn},
		},
		{
			name: "a valid file strategy",
			dir:  filepath.Join("fixture", "valid"),
			want: map[string]Origin{GenericAlert: BuiltIn, "latency-alert": File},
			check: func(t *testing.T, set *Set) {
				t.Helper()

				st, ok := set.Get("latency-alert")
				require.True(t, ok)
				require.Equal(t, filepath.Join("fixture", "valid", "latency-alert.yaml"), st.Path)
				require.Equal(t, fileVersion(t, st.Path), st.Version)
				require.Equal(t, Match{AlertNames: []string{"*Latency*", "*p99*"}, TextPattern: "(?i)latency|slow"}, st.Match)
				require.Equal(t, []string{"signoz", "k8s"}, st.Sources.Prefer)
				require.Len(t, st.First, 2)
				require.Equal(t, "Propose a minimum request rate condition.", st.VerdictHints.FalsePositive)
				require.Equal(t, Budgets{MaxToolCalls: 30, MaxWallTime: 5 * time.Minute}, st.Budgets)
				require.Equal(t, &WatcherSettings{Interval: 20 * time.Second}, st.WatcherDefaults)
				require.Len(t, st.Watchers, 2)
				require.Equal(t, CheckHTTP, st.Watchers[0].Check.Type)
				require.Equal(t, 15*time.Second, st.Watchers[0].Interval)
				require.Equal(t, 3, st.Watchers[0].PassesToRecover)
				require.Equal(t, CheckTool, st.Watchers[1].Check.Type)
				require.Equal(t, "lt", st.Watchers[1].Check.Condition.Op)
			},
		},
		{
			name:    "a file strategy overrides the built-in one with its name and logs it",
			dir:     filepath.Join("fixture", "override"),
			want:    map[string]Origin{GenericAlert: File},
			wantLog: `level=INFO msg="file strategy overrides built-in strategy" strategy=generic-alert file=` + filepath.Join("fixture", "override", "generic-alert.yaml"),
			check: func(t *testing.T, set *Set) {
				t.Helper()

				st, _ := set.Get(GenericAlert)
				require.Equal(t, "The team's own generic strategy.", st.Description)
			},
		},
		{
			name: "hidden files and other extensions are skipped",
			dir:  filepath.Join("fixture", "skipped"),
			want: map[string]Origin{GenericAlert: BuiltIn, "disk-full": File},
		},
		{
			name:    "an empty built-in set",
			builtIn: fstest.MapFS{},
			want:    map[string]Origin{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			set, err := Load(builtInOr(tt.builtIn), tt.dir, slog.New(slog.NewTextHandler(&logs, nil)))
			require.NoError(t, err)

			got := map[string]Origin{}
			for _, name := range set.Names() {
				st, ok := set.Get(name)
				require.True(t, ok)
				require.Equal(t, name, st.Name)
				got[name] = st.Origin
			}

			require.Equal(t, tt.want, got)

			if tt.wantLog == "" {
				require.Empty(t, logs.String())
			} else {
				require.Contains(t, logs.String(), tt.wantLog)
			}

			if tt.check != nil {
				tt.check(t, set)
			}
		})
	}
}

func TestNilSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  *Set
	}{
		{name: "a nil set holds no strategies"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, ok := tt.set.Get(GenericAlert)
			require.False(t, ok)
			require.Empty(t, tt.set.Names())
		})
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		builtIn fs.FS
		dir     string
		want    Errors
	}{
		{
			name: "an unknown key",
			dir:  filepath.Join("fixture", "unknown-key"),
			want: Errors{{File: filepath.Join("fixture", "unknown-key", "generic-alert.yaml"), Message: `line 3: unknown key "frist"`}},
		},
		{
			name: "invalid files report every problem",
			dir:  filepath.Join("fixture", "invalid"),
			want: Errors{
				{File: filepath.Join("fixture", "invalid", "bad-name.yaml"), Message: `name: "Bad_Name" ` + nameRule},
				{File: filepath.Join("fixture", "invalid", "bad-values.yaml"), Message: "match.textPattern: invalid regular expression: error parsing regexp: missing closing ): `(unclosed`"},
				{File: filepath.Join("fixture", "invalid", "bad-values.yaml"), Message: "budgets.maxToolCalls: must not be negative"},
				{File: filepath.Join("fixture", "invalid", "bad-values.yaml"), Message: `watchers[0].check.type: expected one of [http tool alert], got "ping"`},
				{File: filepath.Join("fixture", "invalid", "two-documents.yaml"), Message: "the file must hold one YAML document"},
				{File: filepath.Join("fixture", "invalid", "wrong-type.yaml"), Message: "line 3: cannot unmarshal !!str `check t...` into []string"},
			},
		},
		{
			name: "two files with the same name",
			dir:  filepath.Join("fixture", "duplicate"),
			want: Errors{{File: filepath.Join("fixture", "duplicate", "b.yml"), Message: `strategy "same-name" is already defined in ` + filepath.Join("fixture", "duplicate", "a.yaml")}},
		},
		{
			name:    "an empty built-in file",
			builtIn: fstest.MapFS{"empty.yaml": {Data: nil}},
			want:    Errors{{File: "empty.yaml", Message: "the file is empty"}},
		},
		{
			name:    "a built-in file without a name or description",
			builtIn: fstest.MapFS{"blank.yaml": {Data: []byte("first: [a]\n")}},
			want:    Errors{{File: "blank.yaml", Message: "name: required"}, {File: "blank.yaml", Message: "description: required"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			set, err := Load(builtInOr(tt.builtIn), tt.dir, nil)
			require.Nil(t, set)

			var got Errors
			require.ErrorAs(t, err, &got)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLoadUnreadableDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{
			name:    "the dir is a file",
			dir:     filepath.Join("fixture", "valid", "latency-alert.yaml"),
			wantErr: "load file strategies: read strategies dir " + filepath.Join("fixture", "valid", "latency-alert.yaml") + ": list files:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(strategies.FS, tt.dir, nil)
			require.ErrorContains(t, err, tt.wantErr)

			var problems Errors
			require.NotErrorAs(t, err, &problems)
		})
	}
}

// builtInOr returns fsys, or the real built-in strategies when it is nil.
func builtInOr(fsys fs.FS) fs.FS {
	if fsys == nil {
		return strategies.FS
	}

	return fsys
}

// fileVersion returns the version Load gives the file at path.
func fileVersion(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:versionLength]
}

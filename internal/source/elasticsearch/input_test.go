package elasticsearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSourceAllowed(t *testing.T) {
	t.Parallel()

	patterns := []string{"logs-*", "audit", "*-traces"}

	tests := []struct {
		name  string
		index string
		want  bool
	}{
		{name: "an allowed pattern itself", index: "logs-*", want: true},
		{name: "an index a pattern covers", index: "logs-2026.05.04", want: true},
		{name: "a narrower pattern", index: "logs-checkout-*", want: true},
		{name: "an exact name", index: "audit", want: true},
		{name: "a pattern with a leading wildcard", index: "shop-traces", want: true},
		{name: "a wider pattern", index: "log*"},
		{name: "a name that only starts like an exact one", index: "audit-old"},
		{name: "an index no pattern covers", index: "secrets"},
		{name: "every index", index: "*"},
		{name: "a list", index: "logs-a,secrets"},
		{name: "an exclusion", index: "-logs-a"},
		{name: "a remote cluster", index: "remote:logs-a"},
		{name: "date math", index: "<logs-{now/d}>"},
		{name: "a path", index: "logs-a/_doc"},
		{name: "a single-character wildcard", index: "logs-?"},
		{name: "upper case", index: "LOGS-A"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Source{cfg: Config{IndexPatterns: patterns}}
			require.Equal(t, tt.want, s.allowed(tt.index))
		})
	}
}

func TestSourceQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   string
		wantErr string
	}{
		{name: "a query without _index", query: `level:error AND service.name:"checkout"`},
		{name: "a query over every field", query: `*:timeout`},
		{name: "an allowed _index term", query: `_index:logs-2026.05.04 AND level:error`},
		{name: "an allowed quoted _index", query: `(_index:"logs-a")`},
		{name: "a field that ends in _index", query: `meta._index:secrets`},
		{name: "_index in a message", query: `message:"see_index"`},
		{name: "an _index term outside the allowlist", query: `_index:secrets`, wantErr: "_index:secrets is not an index Sidekick may read"},
		{name: "a second _index term outside the allowlist", query: `_index:logs-a OR _index:secrets`, wantErr: "_index:secrets"},
		{name: "a negated _index term", query: `NOT _index:secrets`, wantErr: "_index:secrets"},
		{name: "a required _index term", query: `+_index:secrets`, wantErr: "_index:secrets"},
		{name: "an _index wildcard field", query: `_*:secrets`, wantErr: "_index:secrets"},
		{name: "a single-character wildcard field", query: `?index:secrets`, wantErr: "_index:secrets"},
		{name: "an escaped character in the field", query: `_ind\ex:secrets`, wantErr: "_index:secrets"},
		{name: "an _index regular expression", query: `_index:/sec.*/`, wantErr: "give _index one index name or pattern"},
		{name: "an _index with an escaped value", query: `_index:logs\-a`, wantErr: "give _index one index name or pattern"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Source{cfg: Config{IndexPatterns: []string{"logs-*"}}}

			_, err := s.query(tt.query)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestIntervalOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seconds int
		length  time.Duration
		want    time.Duration
		wantErr string
	}{
		{name: "an hour", length: time.Hour, want: time.Minute},
		{name: "five minutes", length: 5 * time.Minute, want: 5 * time.Second},
		{name: "a day", length: 24 * time.Hour, want: 30 * time.Minute},
		{name: "seven days", length: maxRange, want: 3 * time.Hour},
		{name: "the call's interval", seconds: 300, length: time.Hour, want: 5 * time.Minute},
		{name: "exactly the most buckets", seconds: 18, length: time.Hour, want: 18 * time.Second},
		{name: "too many buckets", seconds: 17, length: time.Hour, wantErr: "interval: 17s gives 212 buckets, more than 200; use at least 18s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := intervalOf(tt.seconds, window{start: testNow.Add(-tt.length), end: testNow})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

package elasticsearch

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	const base = "https://logs.example.com:9200"

	basic := &BasicAuth{Username: "sidekick", PasswordSecret: passwordRef}
	patterns := []string{"logs-*"}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest Elasticsearch config", cfg: Config{BaseURL: base, APIKeySecret: &apiKeyRef, IndexPatterns: patterns}},
		{
			name: "every key for OpenSearch",
			cfg: Config{
				Flavor:        FlavorOpenSearch,
				BaseURL:       base + "/opensearch/",
				BasicAuth:     basic,
				IndexPatterns: []string{"logs-*", ".ds-traces-*", "audit"},
				TimeField:     "timestamp",
				MessageField:  "log",
				DefaultFields: []string{"kubernetes.*", "level"},
				DiscoverURL:   "https://dashboards.example.com/app/data-explorer/discover#?_q=(query:(language:lucene,query:'{{.Query}}'))",
				Settings:      source.Settings{Timeout: 45 * time.Second},
			},
		},
		{name: "plain http on localhost", cfg: Config{BaseURL: "http://127.0.0.1:9200", BasicAuth: basic, IndexPatterns: patterns}},
		{
			name: "nothing set",
			want: []string{
				"baseURL: required, for example https://logs.example.com:9200",
				"apiKeySecret or basicAuth: set one",
				"indexPatterns: list at least one index pattern",
			},
		},
		{name: "an unknown flavor", cfg: Config{Flavor: "solr", BaseURL: base, BasicAuth: basic, IndexPatterns: patterns},
			want: []string{`flavor: "solr" must be elasticsearch or opensearch`}},
		{name: "a plain http base URL on the network", cfg: Config{BaseURL: "http://logs.example.com:9200", BasicAuth: basic, IndexPatterns: patterns},
			want: []string{`baseURL: "http://logs.example.com:9200" must use https; plain http is only allowed for localhost`}},
		{name: "a base URL with a user", cfg: Config{BaseURL: "https://admin@logs.example.com", BasicAuth: basic, IndexPatterns: patterns},
			want: []string{"must be an absolute https URL, without a query or user"}},
		{name: "a base URL that is not absolute", cfg: Config{BaseURL: "logs.example.com", BasicAuth: basic, IndexPatterns: patterns},
			want: []string{`baseURL: "logs.example.com" must be an absolute https URL`}},
		{name: "both ways to sign in", cfg: Config{BaseURL: base, APIKeySecret: &apiKeyRef, BasicAuth: basic, IndexPatterns: patterns},
			want: []string{"apiKeySecret and basicAuth: set only one"}},
		{name: "an API key for OpenSearch", cfg: Config{Flavor: FlavorOpenSearch, BaseURL: base, APIKeySecret: &apiKeyRef, IndexPatterns: patterns},
			want: []string{"apiKeySecret: only with flavor elasticsearch; use basicAuth for OpenSearch"}},
		{name: "an empty API key reference", cfg: Config{BaseURL: base, APIKeySecret: &registry.SecretRef{}, IndexPatterns: patterns},
			want: []string{"apiKeySecret: name the secret and key"}},
		{name: "basic auth without a user or password", cfg: Config{BaseURL: base, BasicAuth: &BasicAuth{Username: "a:b"}, IndexPatterns: patterns},
			want: []string{"basicAuth.username: required, without a colon", "basicAuth.passwordSecret: required"}},
		{
			name: "index patterns that are not allowed",
			cfg: Config{BaseURL: base, BasicAuth: basic,
				IndexPatterns: []string{"Logs", "logs-*,secrets", "-logs", "_all", "*", ".*", "logs-*", "logs-*", "remote:logs", ".."}},
			want: []string{
				`indexPatterns[0]: "Logs" is not an index pattern`,
				`indexPatterns[1]: "logs-*,secrets" is not an index pattern`,
				`indexPatterns[2]: "-logs" is not an index pattern`,
				`indexPatterns[3]: "_all" is not an index pattern`,
				`indexPatterns[4]: "*" matches every index`,
				`indexPatterns[5]: ".*" matches every index`,
				`indexPatterns[7]: "logs-*" is listed twice`,
				`indexPatterns[8]: "remote:logs" is not an index pattern`,
				`indexPatterns[9]: ".." is not an index pattern`,
			},
		},
		{
			name: "fields that are not names",
			cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns,
				TimeField: "time*", MessageField: "a b", DefaultFields: []string{"ok", "x,y"}},
			want: []string{
				`timeField: "time*" is not a field name`,
				`messageField: "a b" is not a field name`,
				`defaultFields[1]: "x,y" is not a field name`,
			},
		},
		{name: "too many default fields", cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns,
			DefaultFields: strings.Split(strings.Repeat("f,", maxFields)+"g", ",")},
			want: []string{"defaultFields: list at most 50 fields"}},
		{name: "a link template that does not parse", cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns, DiscoverURL: "{{.Query"},
			want: []string{"discoverURL: parse the template"}},
		{name: "a link template with an unknown field", cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns, DiscoverURL: "https://x/{{.Host}}"},
			want: []string{"discoverURL: render the template"}},
		{name: "a link template that is not an absolute URL", cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns, DiscoverURL: "/app/discover"},
			want: []string{`discoverURL: "/app/discover" must render an absolute http or https URL`}},
		{name: "a timeout above the HTTP client's limit", cfg: Config{BaseURL: base, BasicAuth: basic, IndexPatterns: patterns,
			Settings: source.Settings{Timeout: 61 * time.Second}},
			want: []string{"timeout: 1m1s is above 1m0s, the longest the HTTP client waits for response headers"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.want {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestConfigDefaultFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the defaults", want: []string{"@timestamp", "message", "log.level", "service.name", "host.name", "error.message", "trace.id"}},
		{
			name: "configured fields come after the time and message fields, each once",
			cfg:  Config{TimeField: "ts", MessageField: "log", DefaultFields: []string{"level", "ts", "pod"}},
			want: []string{"ts", "log", "level", "pod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.cfg.defaultFields())
		})
	}
}

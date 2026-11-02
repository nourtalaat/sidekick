package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckRouteDetails(t *testing.T) {
	t.Parallel()

	keys := func(n int) map[string]string {
		out := map[string]string{}
		for i := range n {
			out[fmt.Sprintf("key-%d", i)] = "value"
		}

		return out
	}

	tests := []struct {
		name    string
		details map[string]string
		want    []string
	}{
		{name: "none"},
		{name: "a valid set", details: map[string]string{"owner": "payments team", "environment": "prod", "Runbook_1.a-b": "payments-latency page in the on-call wiki"}},
		{name: "the most keys", details: keys(MaxRouteDetails)},
		{name: "one key too many", details: keys(MaxRouteDetails + 1), want: []string{"at most 20 keys are allowed, got 21"}},
		{name: "the longest key", details: map[string]string{strings.Repeat("k", MaxDetailKeyLength): "v"}},
		{
			name:    "a key that is too long",
			details: map[string]string{strings.Repeat("k", MaxDetailKeyLength+1): "v"},
			want:    []string{fmt.Sprintf("key %q must be 1 to 64 characters from letters, digits, \"-\", \"_\" and \".\"", strings.Repeat("k", 65))},
		},
		{name: "an empty key", details: map[string]string{"": "v"}, want: []string{`key "" must be 1 to 64 characters from letters, digits, "-", "_" and "."`}},
		{name: "a key with a space", details: map[string]string{"on call": "v"}, want: []string{`key "on call" must be 1 to 64 characters from letters, digits, "-", "_" and "."`}},
		{name: "a key with a colon", details: map[string]string{"a:b": "v"}, want: []string{`key "a:b" must be 1 to 64 characters from letters, digits, "-", "_" and "."`}},
		{name: "a key with a line break", details: map[string]string{"a\nb": "v"}, want: []string{`key "a\nb" must be 1 to 64 characters from letters, digits, "-", "_" and "."`}},
		{name: "the longest value", details: map[string]string{"k": strings.Repeat("v", MaxDetailValueLength)}},
		{name: "multibyte characters count once", details: map[string]string{"k": strings.Repeat("é", MaxDetailValueLength)}},
		{name: "a value that is too long", details: map[string]string{"k": strings.Repeat("v", MaxDetailValueLength+1)}, want: []string{`key "k": the value must be at most 500 characters, got 501`}},
		{name: "an empty value", details: map[string]string{"k": ""}, want: []string{`key "k": the value must not be empty`}},
		{name: "a value of spaces", details: map[string]string{"k": "  "}, want: []string{`key "k": the value must not be empty`}},
		{name: "a line feed", details: map[string]string{"k": "a\nb"}, want: []string{`key "k": the value must be one line with no control characters`}},
		{name: "a carriage return", details: map[string]string{"k": "a\rb"}, want: []string{`key "k": the value must be one line with no control characters`}},
		{name: "a tab", details: map[string]string{"k": "a\tb"}, want: []string{`key "k": the value must be one line with no control characters`}},
		{name: "a null byte", details: map[string]string{"k": "a\x00b"}, want: []string{`key "k": the value must be one line with no control characters`}},
		{name: "invalid UTF-8", details: map[string]string{"k": "a\xffb"}, want: []string{`key "k": the value must be valid UTF-8 text`}},
		{
			name:    "problems come in key order",
			details: map[string]string{"b": "", "a": "x\ny", "bad key": "v"},
			want: []string{
				`key "a": the value must be one line with no control characters`,
				`key "b": the value must not be empty`,
				`key "bad key" must be 1 to 64 characters from letters, digits, "-", "_" and "."`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, CheckRouteDetails(tt.details))
		})
	}
}

func TestRouteDetailsInTheConfigFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		routes  string
		want    map[string]string
		wantErr string
	}{
		{
			name:   "details are read",
			routes: "- {name: ops, details: {owner: payments team, environment: prod, tier: 1}}\n  - {name: default}",
			want:   map[string]string{"owner": "payments team", "environment": "prod", "tier": "1"},
		},
		{name: "a route without details", routes: "- {name: ops}\n  - {name: default}"},
		{
			name:    "a bad key",
			routes:  "- {name: ops, details: {\"on call\": x}}\n  - {name: default}",
			wantErr: `routes[0].details: key "on call" must be 1 to 64 characters from letters, digits, "-", "_" and "."`,
		},
		{
			name:    "a multi-line value",
			routes:  "- name: ops\n    details:\n      owner: |\n        a\n        b\n  - {name: default}",
			wantErr: `routes[0].details: key "owner": the value must be one line with no control characters`,
		},
		{
			name:    "an empty value",
			routes:  "- {name: ops, details: {owner: \"\"}}\n  - {name: default}",
			wantErr: `routes[0].details: key "owner": the value must not be empty`,
		},
		{
			name:    "the default route is checked too",
			routes:  "- {name: default, details: {owner: \"\"}}",
			wantErr: `routes[0].details: key "owner": the value must not be empty`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), []byte(baseYAML+"\nroutes:\n  "+tt.routes+"\n"), testOptions())
			if tt.wantErr != "" {
				var errs Errors
				require.ErrorAs(t, err, &errs)
				require.Equal(t, tt.wantErr, errs.Error())

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Routes[0].Details)
		})
	}
}

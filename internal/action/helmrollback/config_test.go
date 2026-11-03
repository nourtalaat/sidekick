package helmrollback

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		Limits: Limits{
			Namespaces: []string{"shop"},
			Releases:   []string{"checkout", "web-*"},
		},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("a", maxReleaseNameLength)

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a service account", change: func(c *Config) { c.ServiceAccount = "team-helm" }},
		{name: "the configmap driver", change: func(c *Config) { c.Driver = "configmap" }},
		{name: "the secret driver", change: func(c *Config) { c.Driver = "secret" }},
		{name: "wait turned off", change: func(c *Config) { c.Wait = new(false) }},
		{name: "the longest wait timeout", change: func(c *Config) { c.WaitTimeout = maxWaitTimeout }},
		{name: "the shortest wait timeout", change: func(c *Config) { c.WaitTimeout = time.Nanosecond }},
		{name: "revisions back at the lowest limit", change: func(c *Config) { c.MaxRevisionsBack = new(1) }},
		{name: "revisions back at the highest limit", change: func(c *Config) { c.MaxRevisionsBack = new(action.MaxRevisionsBackCeiling) }},
		{name: "a release name of the longest length", change: func(c *Config) { c.Limits.Releases = []string{longName} }},
		{name: "a release with dots", change: func(c *Config) { c.Limits.Releases = []string{"a.b-c"} }},
		{name: "a pattern of only a star", change: func(c *Config) { c.Limits.Releases = []string{"*"} }},
		{name: "a bad service account", change: func(c *Config) { c.ServiceAccount = "Not_Valid" }, wantErr: "serviceAccount:"},
		{name: "a driver that does not exist", change: func(c *Config) { c.Driver = "sql" }, wantErr: `driver: "sql" must be secret or configmap`},
		{name: "a wait timeout one over the limit", change: func(c *Config) { c.WaitTimeout = maxWaitTimeout + time.Nanosecond }, wantErr: "waitTimeout:"},
		{name: "a negative wait timeout", change: func(c *Config) { c.WaitTimeout = -time.Second }, wantErr: "waitTimeout:"},
		{name: "revisions back of zero", change: func(c *Config) { c.MaxRevisionsBack = new(0) }, wantErr: "maxRevisionsBack:"},
		{name: "revisions back one over the ceiling", change: func(c *Config) { c.MaxRevisionsBack = new(action.MaxRevisionsBackCeiling + 1) }, wantErr: "maxRevisionsBack:"},
		{name: "no namespaces", change: func(c *Config) { c.Limits.Namespaces = nil }, wantErr: "limits.namespaces: at least one entry"},
		{name: "a bad namespace", change: func(c *Config) { c.Limits.Namespaces = []string{"Shop"} }, wantErr: "limits.namespaces:"},
		{name: "a namespace twice", change: func(c *Config) { c.Limits.Namespaces = []string{"a", "a"} }, wantErr: `limits.namespaces: "a" is listed twice`},
		{name: "no releases", change: func(c *Config) { c.Limits.Releases = nil }, wantErr: "limits.releases: at least one entry"},
		{name: "a release name one over the length", change: func(c *Config) { c.Limits.Releases = []string{longName + "a"} }, wantErr: "limits.releases:"},
		{name: "an upper case release", change: func(c *Config) { c.Limits.Releases = []string{"Checkout"} }, wantErr: "limits.releases:"},
		{name: "a release that ends with a dash", change: func(c *Config) { c.Limits.Releases = []string{"a-"} }, wantErr: "limits.releases:"},
		{name: "a pattern with a question mark", change: func(c *Config) { c.Limits.Releases = []string{"a?"} }, wantErr: "limits.releases:"},
		{name: "a pattern with a bracket", change: func(c *Config) { c.Limits.Releases = []string{"a[b]"} }, wantErr: "limits.releases:"},
		{name: "an empty release", change: func(c *Config) { c.Limits.Releases = []string{""} }, wantErr: "limits.releases:"},
		{name: "a release twice", change: func(c *Config) { c.Limits.Releases = []string{"a", "a"} }, wantErr: `limits.releases: "a" is listed twice`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validConfig()
			tt.change(&c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		change      func(c *Config)
		wantAccount string
		wantDriver  string
		wantWait    bool
		wantTimeout time.Duration
		wantMax     int
	}{
		{
			name: "nothing set", change: func(*Config) {},
			wantAccount: "sidekick-helm-rollback", wantDriver: "secret", wantWait: true, wantTimeout: 3 * time.Minute, wantMax: 1,
		},
		{
			name: "everything set",
			change: func(c *Config) {
				c.ServiceAccount, c.Driver, c.Wait, c.WaitTimeout, c.MaxRevisionsBack = "team-helm", "configmap", new(false), time.Minute, new(4)
			},
			wantAccount: "team-helm", wantDriver: "configmap", wantWait: false, wantTimeout: time.Minute, wantMax: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validConfig()
			tt.change(&c)

			require.Equal(t, tt.wantAccount, c.serviceAccount())
			require.Equal(t, tt.wantDriver, c.driver())
			require.Equal(t, tt.wantWait, c.wait())
			require.Equal(t, tt.wantTimeout, c.waitTimeout())
			require.Equal(t, tt.wantMax, c.Max())
		})
	}
}

func TestConfigReadsYAML(t *testing.T) {
	t.Parallel()

	const section = `
allowReadWrite: true
maxRevisionsBack: 2
serviceAccount: team-helm
driver: configmap
wait: false
waitTimeout: 90s
limits:
  namespaces: [shop]
  releases: [checkout, "web-*"]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		ReadWriteGate:  action.ReadWriteGate{AllowReadWrite: true},
		RevisionLimit:  action.RevisionLimit{MaxRevisionsBack: new(2)},
		ServiceAccount: "team-helm",
		Driver:         "configmap",
		Wait:           new(false),
		WaitTimeout:    90 * time.Second,
		Limits:         Limits{Namespaces: []string{"shop"}, Releases: []string{"checkout", "web-*"}},
	}, c)
}

func TestConfigRefusesKeysThatAreNotConfigurable(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"force", "recreate", "disableHooks"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			dec := yaml.NewDecoder(strings.NewReader(key + ": true\n"))
			dec.KnownFields(true)

			var c Config

			require.ErrorContains(t, dec.Decode(&c), "field "+key+" not found")
		})
	}
}

func TestConfigIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "without the opt-in", yaml: "driver: secret", want: true},
		{name: "with the opt-in", yaml: "allowReadWrite: true", want: false},
		{name: "with the opt-in turned off", yaml: "allowReadWrite: false", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c Config

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &c))
			require.Equal(t, tt.want, action.ReadWriteBlocked(&c))
		})
	}
}

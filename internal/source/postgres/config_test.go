package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testURLRef is the secret reference of a test database's URL.
var testURLRef = registry.SecretRef{Name: "orders-db", Key: "url"}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	orders := Database{Name: "orders", URLSecret: testURLRef}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest config", cfg: Config{Databases: []Database{orders}}},
		{
			name: "several databases, text left out and a longer statement timeout",
			cfg: Config{
				Settings:         source.Settings{Timeout: time.Minute},
				Databases:        []Database{orders, {Name: "billing_replica", URLSecret: testURLRef, AllowInsecure: true}},
				OmitQueryText:    true,
				StatementTimeout: 50 * time.Second,
			},
		},
		{name: "no databases", want: []string{"databases: list at least one database"}},
		{
			name: "a database without a name or a secret",
			cfg:  Config{Databases: []Database{{}}},
			want: []string{"databases[0].name: required", "databases[0].urlSecret: required"},
		},
		{
			name: "names the model could not use",
			cfg:  Config{Databases: []Database{{Name: "Orders", URLSecret: testURLRef}, {Name: "1st", URLSecret: testURLRef}}},
			want: []string{
				`databases[0].name: "Orders" must start with a lower-case letter`,
				`databases[1].name: "1st" must start with a lower-case letter`,
			},
		},
		{
			name: "two databases with one name",
			cfg:  Config{Databases: []Database{orders, orders}},
			want: []string{`databases[1].name: "orders" is already the name of another database`},
		},
		{
			name: "a negative statement timeout",
			cfg:  Config{Databases: []Database{orders}, StatementTimeout: -time.Second},
			want: []string{"statementTimeout: must not be negative"},
		},
		{
			name: "a statement timeout as long as the call timeout",
			cfg:  Config{Databases: []Database{orders}, StatementTimeout: 30 * time.Second},
			want: []string{"statementTimeout: 30s must be shorter than the call timeout, 30s"},
		},
		{
			name: "a call timeout shorter than the default statement timeout",
			cfg:  Config{Databases: []Database{orders}, Settings: source.Settings{Timeout: 5 * time.Second}},
			want: []string{"statementTimeout: 10s must be shorter than the call timeout, 5s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			lines := strings.Split(err.Error(), "\n")
			require.Len(t, lines, len(tt.want))

			for i, want := range tt.want {
				require.Contains(t, lines[i], want)
			}
		})
	}
}

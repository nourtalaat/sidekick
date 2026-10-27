package doctor

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
)

var (
	errRefused = errors.New("dial tcp 10.0.0.5:5432: connection refused")
	errNoTable = errors.New("relation goose_db_version does not exist")
)

func TestCheckDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		manual     bool
		noSecret   bool
		openErr    error
		setupMocks func(db *MockDatabase)
		want       Result
	}{
		{
			name: "the schema is current",
			setupMocks: func(db *MockDatabase) {
				db.EXPECT().SchemaVersion(gomock.Any()).Return(int64(7), int64(7), nil)
			},
			want: Result{Check: "database", Status: StatusOK, Message: "connected; the schema has every migration this release needs (7)"},
		},
		{
			name: "the schema is newer",
			setupMocks: func(db *MockDatabase) {
				db.EXPECT().SchemaVersion(gomock.Any()).Return(int64(8), int64(7), nil)
			},
			want: Result{Check: "database", Status: StatusOK, Message: "connected; the schema is at migration 8, newer than the 7 this release needs, which is fine during a rolling update"},
		},
		{
			name: "the schema is behind with startup migrations",
			setupMocks: func(db *MockDatabase) {
				db.EXPECT().SchemaVersion(gomock.Any()).Return(int64(5), int64(7), nil)
			},
			want: Result{
				Check: "database", Status: StatusFail,
				Message: "connected, but the database has migrations up to 5 and this release needs 7",
				Fix:     "restart Sidekick, which migrates at start-up, or run `sidekick migrate up`",
			},
		},
		{
			name:   "the schema is behind with manual migrations",
			manual: true,
			setupMocks: func(db *MockDatabase) {
				db.EXPECT().SchemaVersion(gomock.Any()).Return(int64(5), int64(7), nil)
			},
			want: Result{
				Check: "database", Status: StatusFail,
				Message: "connected, but the database has migrations up to 5 and this release needs 7",
				Fix:     "database.migrations is manual, so run `sidekick migrate up`",
			},
		},
		{
			name: "the schema version cannot be read",
			setupMocks: func(db *MockDatabase) {
				db.EXPECT().SchemaVersion(gomock.Any()).Return(int64(0), int64(0), errNoTable)
			},
			want: Result{
				Check: "database", Status: StatusFail,
				Message: "connected, but cannot read the schema version: " + errNoTable.Error(),
				Fix:     "check that the database user can read the goose_db_version table",
			},
		},
		{
			name:    "the database cannot be reached",
			openErr: errRefused,
			want: Result{
				Check: "database", Status: StatusFail,
				Message: "cannot connect: " + errRefused.Error(),
				Fix:     "check the URL in the Secret sidekick-db, key url, and that Postgres 15 or newer accepts connections from Sidekick's pod",
			},
		},
		{
			name:     "the URL secret is missing",
			noSecret: true,
			want: Result{
				Check: "database", Status: StatusFail,
				Message: "database.urlSecret: cannot read secret sidekick-db/url: the file does not exist",
				Fix:     secretFix(config.Database{}.URLSecret),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			db := NewMockDatabase(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(db)
				db.EXPECT().Close()
			}

			files := maps.Clone(defaultSecrets)
			if tt.noSecret {
				delete(files, "sidekick-db/url")
			}

			content := baseConfig
			if tt.manual {
				content = strings.Replace(content, "  urlSecret:", "  migrations: manual\n  urlSecret:", 1)
			}

			cfg := parseConfig(t, content, files, config.Registries{})

			var gotURL string

			d := newDoctor(Options{Config: cfg, OpenDatabase: func(_ context.Context, url string) (Database, error) {
				gotURL = url
				if tt.openErr != nil {
					return nil, tt.openErr
				}

				return db, nil
			}})

			got := d.checkDatabase(t.Context())

			want := tt.want
			if tt.noSecret {
				want.Fix = secretFix(cfg.Database.URLSecret)
			} else {
				require.Equal(t, testDBURL, gotURL)
			}

			require.Equal(t, want, got)
		})
	}
}

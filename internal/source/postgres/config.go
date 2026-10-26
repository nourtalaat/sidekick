package postgres

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultStatementTimeout bounds one query when the config sets no
// statementTimeout.
const defaultStatementTimeout = 10 * time.Second

// databaseNamePattern is the shape of a database's name in the config,
// which the model passes to pick it.
var databaseNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// Config is the config section of one Postgres source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Databases are the databases the tools may read. The model picks one
	// by name on each call.
	Databases []Database `yaml:"databases"`
	// OmitQueryText leaves the text of queries out of every result, for
	// databases whose queries hold literal values that must not leave
	// them. The guard redacts what it recognizes either way.
	OmitQueryText bool `yaml:"omitQueryText"`
	// StatementTimeout bounds each query Sidekick runs. Zero means
	// defaultStatementTimeout. It must be shorter than the call timeout.
	StatementTimeout time.Duration `yaml:"statementTimeout"`
}

// Database is one database the tools may read.
type Database struct {
	// Name is what the model calls the database, for example orders.
	Name string `yaml:"name"`
	// URLSecret holds the postgres:// URL to connect with, user and
	// password included.
	URLSecret registry.SecretRef `yaml:"urlSecret"`
	// AllowInsecure allows a URL that does not verify the server's TLS
	// certificate, for a server that cannot offer one. Sidekick logs a
	// warning each time it connects with such a URL. A loopback host or a
	// Unix socket needs no TLS and no opt-out.
	AllowInsecure bool `yaml:"allowInsecure"`
}

// Validate checks the section without secrets or network calls: at least
// one database is listed, each has a valid name that no other uses and a
// URL secret, and the statement timeout fits the call timeout. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.StatementTimeout < 0 {
		errs = append(errs, errors.New("statementTimeout: must not be negative"))
	}

	if c.statementTimeout() >= c.CallTimeout() {
		errs = append(errs, fmt.Errorf("statementTimeout: %s must be shorter than the call timeout, %s", c.statementTimeout(), c.CallTimeout()))
	}

	if len(c.Databases) == 0 {
		errs = append(errs, errors.New("databases: list at least one database"))
	}

	seen := map[string]bool{}

	for i, db := range c.Databases {
		key := fmt.Sprintf("databases[%d]", i)

		switch {
		case db.Name == "":
			errs = append(errs, fmt.Errorf("%s.name: required", key))
		case !databaseNamePattern.MatchString(db.Name):
			errs = append(errs, fmt.Errorf("%s.name: %q must start with a lower-case letter and hold only lower-case letters, digits, _ and -, at most 63 in all", key, db.Name))
		case seen[db.Name]:
			errs = append(errs, fmt.Errorf("%s.name: %q is already the name of another database", key, db.Name))
		}

		seen[db.Name] = true

		if db.URLSecret == (registry.SecretRef{}) {
			errs = append(errs, fmt.Errorf("%s.urlSecret: required", key))
		}
	}

	return errors.Join(errs...)
}

// statementTimeout returns the statement timeout, with the default
// applied.
func (c *Config) statementTimeout() time.Duration {
	if c.StatementTimeout > 0 {
		return c.StatementTimeout
	}

	return defaultStatementTimeout
}

// database returns the database with the given name.
func (c *Config) database(name string) (Database, bool) {
	for _, db := range c.Databases {
		if db.Name == name {
			return db, true
		}
	}

	return Database{}, false
}

// databaseNames returns the names of the databases, in config order.
func (c *Config) databaseNames() []string {
	names := make([]string, 0, len(c.Databases))
	for _, db := range c.Databases {
		names = append(names, db.Name)
	}

	return names
}

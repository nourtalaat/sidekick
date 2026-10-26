//go:build integration

package store

// MigrateFS lets the tests apply their own migrations.
var MigrateFS = migrate

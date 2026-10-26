// Package storetest gives each integration test its own real, migrated
// Postgres database. It is built only with the `integration` build tag, so
// only integration tests can use it.
//
// The tests use the Postgres server at SIDEKICK_TEST_DATABASE_URL when it is
// set (as in CI), and otherwise start one in a container with
// testcontainers-go. The first test in a test binary builds a template
// database with every migration applied; each test then gets a copy of it,
// which is much faster than migrating a new database every time.
//
// A package that uses storetest has a setup_test.go with a TestMain that
// calls Main, and keeps its seed data in a fixture/ directory. The tests of
// this package are the example to copy.
package storetest

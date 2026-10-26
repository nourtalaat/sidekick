# Contributing to Sidekick

Thank you for helping. This guide covers how to build and test Sidekick, how
to name branches, commits and pull requests, and the rules for tests and Go
code that review checks.

## Set up

You need:

- Go 1.26 or newer, and a C compiler (sqlc is built with cgo).
- GNU Make and Git.
- For integration tests, one of:
  - Docker or Podman, so the tests can start Postgres in a container
    themselves; or
  - a Postgres 15 or newer server (CI uses 16), given as a database URL in
    `SIDEKICK_TEST_DATABASE_URL`, for example
    `postgres://sidekick:sidekick@localhost:5432/sidekick_test?sslmode=disable`.
    The user must be allowed to create databases.

The Makefile installs its own pinned copies of golangci-lint, mockgen, sqlc
and govulncheck under `bin/tools`, so you do not need to install them
yourself.

### Postgres for integration tests with Podman

With Docker running, `make test-integration` needs no setup. With Podman,
point the tests at the Podman machine's API socket and turn off Ryuk, the
clean-up container of testcontainers-go, which does not work on Podman's
default network:

```sh
export DOCKER_HOST="unix://$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}')"
export TESTCONTAINERS_RYUK_DISABLED=true
```

Without Ryuk, each test binary still stops its own container when it ends.
A test run that is killed part way can leave one running; `podman ps` lists
it and `podman stop <id>` stops it.

Each package starts its own container, which takes a few seconds. To skip
that, run one Postgres yourself and point the tests at it:

```sh
podman run -d --name sidekick-test-postgres -p 5432:5432 \
  -e POSTGRES_USER=sidekick -e POSTGRES_PASSWORD=sidekick \
  -e POSTGRES_DB=sidekick_test postgres:16
export SIDEKICK_TEST_DATABASE_URL='postgres://sidekick:sidekick@localhost:5432/sidekick_test?sslmode=disable'
```

The tests keep a template database named `sidekick_template_<hash>` on that
server and make a new one whenever the migrations change. Old ones are safe
to drop.

## Make targets

Run `make help` for the full list. The main ones:

| Target | Does |
|---|---|
| `make ci` | Everything CI runs. Run it before every push. |
| `make lint` | golangci-lint, and a check that `go.mod` and `go.sum` are tidy. |
| `make fmt` | Formats the Go code (gofmt and goimports). |
| `make test` | Unit tests with the race detector. |
| `make test-integration` | Unit and integration tests (build tag `integration`) against Postgres. |
| `make generate` | Every code generator: `generate-mocks`, `sqlc-generate` and `notice`. |
| `make generate-mocks` | Runs the `mockgen` directives through `go generate`. |
| `make sqlc-generate` | Generates `internal/store/db` from the queries and migrations. |
| `make notice` | Writes `NOTICE`, the licenses of every module linked into the binary. Run it after changing dependencies. |
| `make migrate-new name=...` | Creates the next migration file in `internal/store/migrations`. |
| `make verify-generated` | Runs `make generate` and fails if any file changed. |
| `make build` | Builds `bin/sidekick` with the version and commit set. |
| `make vulncheck` | govulncheck on the code and its dependencies. |
| `make image` | Builds the container image for linux/amd64 and linux/arm64 with Docker, or Podman when Docker is missing. `IMAGE_PLATFORMS=linux/arm64` builds one platform. |

To add a target to `make ci`, put `CI_TARGETS += <name>` above its rule. A new
code generator adds itself to `make generate` the same way, with
`GENERATE_TARGETS += <name>`.

## Branches, commits and pull requests

Work is tracked as issues with IDs of the form `SK-<n>`. The repository turns
`SK-<n>` into a link to the issue.

- `main` is the only long-lived branch. Branch off it and merge back into it
  with a squash merge.
- Name branches `<feature|improvement|fix>/SK-<n>/<short-name>`, for example
  `feature/SK-12/webhook-intake`.
- Split work into small commits, one logical change each. Every commit must
  build and pass its own tests, so it can be reviewed and reverted on its own.
- Start each commit subject with the issue ID and an imperative verb:
  `[SK-12] Add webhook intake`. Use the body to say why the change is needed.
- Commits carry only the author's own name: no AI attribution trailers such
  as `Co-Authored-By` lines for a coding assistant.
- Title pull requests the same way, in sentence case:
  `[SK-12] Add webhook intake`. Fill in the pull request template.
- Once a branch is pushed, do not force-push or rewrite its history. Add new
  commits instead; the squash merge combines them.
- Before every push, merge the latest `origin/main` into your branch and run
  `make ci` again.

## Unit tests

- Assert with `github.com/stretchr/testify/require`, not `assert`. The linter
  rejects `assert`.
- Write table tests. Call `t.Parallel()` at the top of the test function and
  at the top of each `t.Run` closure. The linter checks both.
- Mock with gomock from `go.uber.org/mock`, not `github.com/golang/mock`.
  - Make one controller per subtest with `gomock.NewController(t)`. It checks
    the expectations when the test ends, so do not call `Finish()`.
  - When every case needs the same mocks, make them inside the `t.Run`
    closure. When cases need different expectations, give the table a
    `setupMocks` field.
  - When one instance under test is shared by all cases, replace its mocked
    fields by hand in each case.
- When you add an interface that needs a mock, put this line above the
  package clause of its file, then run `make generate-mocks` and commit the
  result:

  ```go
  //go:generate mockgen -source <file>.go -destination mocks_<file>.go -package <pkg> -write_package_comment=false
  ```

  `-write_package_comment=false` keeps the mock file from adding a second
  package doc comment. A file that also holds a sealed interface (one with an
  unexported method, such as `model.Part`) adds
  `-exclude_interfaces <Name>`, so the mock does not join the sealed set.

## Third-party services in tests

Tests never call a real third-party service. Mock it with
`internal/fakehttp`, in unit and integration tests alike: each test or
subtest starts its own server, so nothing is shared and tests stay
parallel. Do not use gock or another library that keeps its mocks in one
process-wide list; such mocks race as soon as tests run in parallel.

```go
api := fakehttp.New(t)
api.Handle(http.MethodPost, "/v1/messages",
	fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "1"}},
	fakehttp.Response{BodyFile: "fixture/text.sse", Headers: map[string]string{"Content-Type": "text/event-stream"}},
)
api.ExpectAllCalled()

client := NewClient(api.URL(), api.Client())
```

- `Handle` serves its responses once each, in order. `HandleFunc` takes a
  handler function for anything canned responses cannot do. `Response`
  also has `Body` and `Delay`, and `fakehttp.New(t, fakehttp.WithTLS())`
  starts an HTTPS server.
- A request that no route expects fails the test and gets a 501, so a
  missing mock never passes silently. `ExpectAllCalled` also fails the test
  when a response goes unused.
- `Requests` returns every request received, with its method, path, query,
  headers and body, for assertions on what the code sent.
- Keep recorded responses in the package's `fixture/` directory; a
  `BodyFile` path is relative to the package.
- The tests of `internal/model/anthropic` show the pattern.

## Database

Sidekick uses only Postgres, through pgx. The schema lives in goose
migrations, queries are `.sql` files that sqlc turns into Go, and IDs are
UUIDv7 values from `internal/ids` (`ids.New()`).

### Adding a migration

1. Run `make migrate-new name=add_jobs`. It creates
   `internal/store/migrations/<next number>_add_jobs.sql`.
2. Write the change under `-- +goose Up`, and the way to undo it under
   `-- +goose Down`. Wrap a statement that holds semicolons of its own, such
   as a function body, in `-- +goose StatementBegin` and
   `-- +goose StatementEnd`.
3. Run `make sqlc-generate`, because sqlc reads the migrations as the
   schema, and commit the migration with the generated code.

Rules:

- A migration only adds things: tables, nullable columns, indexes. Code in
  one release must work with the schema of the release before it, so a
  rolling update is safe. Dropping a table or column ships in a later,
  separate change once nothing uses it.
- Create indexes on existing tables with `CREATE INDEX CONCURRENTLY`, in a
  migration that starts with `-- +goose NO TRANSACTION`.
- Until the first tagged release, v0.1.0, existing migration files may be
  edited. From v0.1.0 on, migrations are only added, never edited.
- Migration numbers run 1, 2, 3 with no gaps. If your branch and `main`
  both add the same number, the migrations unit test fails after you merge
  `main`; renumber yours to the next free number.

The binary embeds the migrations. `sidekick migrate up` applies them and
`sidekick migrate status` lists them. Both read the config file given with
`--config` (default `/etc/sidekick/sidekick.yaml`) and take the database URL
from the secret that `database.urlSecret` names; there is no flag or
environment variable for it, so the password never shows in the process
list. Only one process migrates at a time: goose holds a Postgres advisory
lock while it runs. `sidekick serve` also migrates at start-up, unless
`database.migrations` is `manual`.

### Adding a query

1. Put the query in a `.sql` file in `internal/store/queries`, one file per
   table or topic, with a name and a result kind above it:

   ```sql
   -- name: GetJob :one
   SELECT * FROM jobs WHERE id = $1;
   ```

   Use `:one` for one row, `:many` for several, and `:exec` when nothing
   comes back. Name parameters with `@name` or `sqlc.arg(name)` when `$1`
   would be unclear.
2. Run `make sqlc-generate` and commit the changes in `internal/store/db`.
   `make verify-generated` (part of `make ci`) fails if you forget.
3. Call it through `db.New(pool)`, and use the generated row and parameter
   types directly rather than copying them into types of your own.
4. Put the query's `EXPLAIN ANALYZE` output, against realistic data, in the
   pull request description.

## Background jobs

Work that does not finish inside a request runs as a job from
`internal/queue`, backed by the `jobs` table.

- Add a job with `queue.Enqueue(ctx, tx, queue.NewJob{...})`. Pass the
  transaction that writes the row the job is about, so the row and the job
  commit together. With a `UniqueKey` that a pending or running job already
  has, `Enqueue` returns `queue.ErrDuplicate` and the transaction stays
  usable.
- Handle a kind by registering a `queue.Handler` on the `queue.Runner`. Mark
  a kind `Long` in its `KindOptions` when a run can take more than a few
  minutes, so its lease is renewed while it runs.
- A handler can run more than once for the same job, after a crash or a
  lost lease, so make it safe to repeat. Fence your own writes with
  `job.ClaimToken`: a worker that lost its lease then stops writing.
- Return an error to retry the job with backoff. Wrap `queue.ErrGiveUp`
  into the error when a retry cannot help, for example a payload that does
  not decode, so the job becomes dead at once.
- Return soon after the handler's context ends. At shutdown, the context
  ends 30 seconds after `SIGTERM`; a handler that then returns an error has
  its job put back in the queue with its attempts unchanged.

## Integration tests

- Every HTTP endpoint gets an integration test.
- Integration tests run against a real Postgres. Put them in files with the
  `//go:build integration` constraint; `make test-integration` runs them.
- Get a database from `internal/store/storetest`: `storetest.New(t)` returns
  a pool on a new database of the test's own, with every migration applied,
  and drops it when the test ends. Tests can therefore call `t.Parallel()`.
  `storetest.LoadFixture(t, pool, "fixture/<file>.sql")` runs a seed file.
- Mock only third-party services (model providers, Slack, GitHub and so on),
  with `internal/fakehttp`, as in
  [Third-party services in tests](#third-party-services-in-tests).
  Everything inside Sidekick runs for real.
- Each package with integration tests has:
  - a `fixture/` directory with database seed data, request and expected
    response JSON, mocked third-party responses, and config such as feature
    flag state;
  - a `setup_test.go` that seeds the database and wires up the services and
    handlers, with a `TestMain` that calls `storetest.Main`, so the test
    binary stops any Postgres container it started;
  - the test files themselves.

  The tests of `internal/store/storetest` are a small example of this layout
  to copy.

## Go style

- Wrap every error you pass up with a short description of what failed and
  `%w`. Never return a caught error bare:

  ```go
  if err != nil {
  	return fmt.Errorf("load config %s: %w", path, err)
  }
  ```

  Returning a sentinel error or a newly built error where it starts is fine.
  The linter (`wrapcheck`) catches unwrapped errors from other packages;
  review catches the rest.
- Leave one blank line after a closing brace that ends a block (`if`, `for`,
  `switch`, `select`, a function literal used as a statement) before the next
  statement at the same level. No blank line is needed when the next line is
  another closing brace, `else`, `case`, `default` or a closing parenthesis.
  The linter checks this.
- Order code in a file so it reads from the top down:
  1. A caller comes before the functions it calls. This rule wins over the
     others.
  2. Methods come before plain functions.
  3. Exported names come before unexported ones.
  4. Small helpers go at the bottom of the file or in their own file.
- Keep comments few. Doc comments on exported names are always welcome. Do
  not write comments that repeat what the code says; if code needs a comment
  to be understood, rename things or restructure it instead.
- Mark deprecated code with the godoc `Deprecated:` marker.

## Package dependencies

The plugin packages under `internal/` import each other in one direction only:
`event` is imported by `source`, which is imported by `report`, then `action`,
then `output`, then `registry`. `model` and `intake` import only `event`, or
nothing. Plugins in subpackages (for example `internal/model/anthropic`)
import `registry` and register themselves in `init`. The linter (`depguard`)
rejects imports that go the other way.

## Adding HTTP routes and job handlers

`internal/app` starts and stops everything; `cmd/sidekick/serve.go` makes
the app with `app.New`, and a feature adds itself before `Run`:

```go
if err := a.RegisterRoutes(func(mux *http.ServeMux) {
	mux.Handle("POST /hooks/{name}", hooks)
}); err != nil {
	return fmt.Errorf("register webhook routes: %w", err)
}

if err := a.RegisterJob(queue.KindRouteEvent, router, queue.KindOptions{}); err != nil {
	return fmt.Errorf("register the router: %w", err)
}
```

Routes go on the main listener (`server.listen`); the metrics listener is
reserved for `/metrics`, `/healthz` and `/readyz`. The job queue runs only
when at least one handler is registered. Get the config, logger, metrics
registry, tracer provider, database pool, secrets resolver (`Secrets`),
redactor (`Redactor`) and plugin instances from the app's accessors rather
than from globals.

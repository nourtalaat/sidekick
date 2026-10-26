# Contributing to Sidekick

Thank you for helping. This guide covers how to build and test Sidekick, how
to name branches, commits and pull requests, and the rules for tests and Go
code that review checks.

## Set up

You need:

- Go 1.26 or newer.
- GNU Make and Git.
- Postgres 16 for integration tests, given as a database URL in
  `SIDEKICK_TEST_DATABASE_URL`, for example
  `postgres://sidekick:sidekick@localhost:5432/sidekick_test?sslmode=disable`.

The Makefile installs its own pinned copies of golangci-lint, mockgen and
govulncheck under `bin/tools`, so you do not need to install them yourself.

## Make targets

Run `make help` for the full list. The main ones:

| Target | Does |
|---|---|
| `make ci` | Everything CI runs. Run it before every push. |
| `make lint` | golangci-lint, and a check that `go.mod` and `go.sum` are tidy. |
| `make fmt` | Formats the Go code (gofmt and goimports). |
| `make test` | Unit tests with the race detector. |
| `make test-integration` | Unit and integration tests (build tag `integration`) against Postgres. |
| `make generate` | Every code generator, for now `generate-mocks`. |
| `make generate-mocks` | Runs the `mockgen` directives through `go generate`. |
| `make verify-generated` | Runs `make generate` and fails if any file changed. |
| `make build` | Builds `bin/sidekick` with the version and commit set. |
| `make vulncheck` | govulncheck on the code and its dependencies. |

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
  //go:generate mockgen -source <file>.go -destination mocks_<file>.go -package <pkg>
  ```

## Integration tests

- Every HTTP endpoint gets an integration test.
- Integration tests run against a real Postgres. Put them in files with the
  `//go:build integration` constraint; `make test-integration` runs them.
- Mock only third-party services (model providers, Slack, GitHub and so on),
  with `gock`. Everything inside Sidekick runs for real.
- Each package with integration tests has:
  - a `fixture/` directory with database seed data, request and expected
    response JSON, mocked third-party responses, and config such as feature
    flag state;
  - a `setup_test.go` that seeds the database and wires up the services and
    handlers;
  - the test files themselves.

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

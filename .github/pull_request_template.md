<!--
Title: [SK-<n>] <Verb> <subject>, in sentence case.
Use Problem and Solution for a fix, or Context and Changes for a feature or
an improvement. Delete the sections you do not use.
-->

## Context

<!-- Which issue (SK-<n>) and why the change is needed. -->

## Changes

<!-- What changed, in a few bullets. -->

## Deployment notes

<!-- Migrations, config changes or anything an operator must do. Delete if none. -->

## Checklist

- [ ] `make ci` passes locally.
- [ ] Unit tests follow CONTRIBUTING.md (table tests, `t.Parallel()`, `require`, gomock).
- [ ] New HTTP endpoints have integration tests.
- [ ] Every new query has its `EXPLAIN ANALYZE` output in this description.
- [ ] Migrations only add things; anything dropped ships in a later change.
- [ ] User-facing changes update the docs under `docs/`.

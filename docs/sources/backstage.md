# The Backstage source

The Backstage source (`type: backstage`) reads a Backstage software catalog,
so an investigation can find out who owns a service, which system it is
part of, and which services and resources it depends on. Sidekick also
reads the catalog's dependencies in the background to group alerts of
services that depend on each other; see
[Grouping](../configuration.md#grouping). Every tool only reads. This page
lists its keys, its tools and the token it needs.

## Config

```yaml
sources:
  backstage:
    type: backstage
    baseURL: https://backstage.example.com
    tokenSecret: { name: sidekick-backstage, key: token }
```

| Key | Default | Meaning |
|---|---|---|
| `baseURL` | none | The root of the Backstage backend. Required. The catalog API is under `/api/catalog`. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the token is sent with each request. |
| `tokenSecret` | none | `{ name, key }` of the Secret that holds the token Sidekick sends as a bearer token. Required. |
| `appURL` | `baseURL` | The root of the Backstage web app, for links to entity pages, when the web app and the backend have different hosts. |
| `namespace` | `default` | The catalog namespace of a name given without one. |

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`backstage__get_service`.

| Tool | Returns |
|---|---|
| `get_service` | A component, resource or API: its owners, systems, type and lifecycle, what it depends on, what depends on it, the APIs it provides and consumes, its tags and its links. A bare name is a component; `resource:orders-db` or `component:shop/checkout-api` name others. |
| `get_owner` | A group or user: its name, email, parent and child groups, members, the groups it is a member of, and what it owns. A bare name is a group. |
| `get_system` | A system: its owners, its domain and its parts. |

How it behaves:

- **Start-up never waits for Backstage.** The first tool call is the first
  request. The token is read from its Secret for each request, so a
  rotated token needs no restart.
- **Empty results.** An entity the catalog does not have returns no data,
  not an error. So does one the token may not read, because Backstage
  answers both the same way.
- **Errors the model can act on.** A name that is not a valid catalog name,
  a kind the tool does not read, or a request Backstage refuses as bad is a
  tool error, and a bad name is never sent to Backstage.
- **Failures.** A refused token, a server error or a Backstage that cannot
  be reached fails the call. After 5 failures in a row the source is
  skipped for 60 seconds, as for every source.
- **Evidence.** Each result links to the entity's page in the Backstage web
  app.
- **Size.** Lists keep at most 100 entries, and long descriptions and names
  are cut; a result that was cut is marked as cut.

## Dependencies for grouping

Every 10 minutes, each Sidekick replica reads all components and resources
of the catalog, in pages of 500, and keeps their `dependsOn` and
`dependencyOf` relations. A service is named by its entity name without
kind or namespace, so the names in the catalog must match the `service`
that alerts carry. Relations to APIs are left out. When a read fails,
Sidekick keeps the dependencies it read last and logs a warning.

## Token

Backstage needs a token for service-to-service calls. With the new backend
system, add a static token for Sidekick under `backend.auth.externalAccess`
in `app-config.yaml`, and allow it only the catalog plugin:

```yaml
backend:
  auth:
    externalAccess:
      - type: static
        options:
          token: ${SIDEKICK_BACKSTAGE_TOKEN}
          subject: sidekick
        accessRestrictions:
          - plugin: catalog
            permission: catalog.entity.read
```

Put the same token in the Secret that `tokenSecret` names. Sidekick only
sends `GET` requests to the catalog API, so read access is all it needs.

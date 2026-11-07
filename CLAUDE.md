# gitopsify-provider-grafana

The gitopsify provider for Grafana: reads a Grafana over its HTTP API and
describes its objects as grafana/grafana Terraform resources, which gitopsify
writes as Terraform or Crossplane. Protocol and SDK:
`../gitopsify/sdk` and `../gitopsify/docs/writing-providers.md`.

## Every session

1. **Start:** read [HANDOFF.md](HANDOFF.md) with the `session-handoff` skill.
2. **End:** update HANDOFF.md before you stop, even when unfinished.
3. Have a fresh-context reviewer subagent review changes before calling them done.

## Commands

- `make build test lint`; `make install` puts the binary on PATH for `gitopsify export grafana`
- Local Grafana: `docker run -d --name grafana -p 3300:3000 -e GF_SECURITY_ADMIN_PASSWORD=admin grafana/grafana:13.3.0`,
  then `GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin test/live/seed.sh`
- `make record` after changing what the provider reads (re-records testdata/api)
- `make upstream` after bumping the pinned upstream versions or adding a kind
  (refreshes testdata/upstream; `tools/upstream` is a copy of the template's,
  keep them in sync)
- `make live`: the full live test (kind + Crossplane + Terraform + OpenTofu) on a throwaway Hetzner machine

## Where things are

- `internal/grafana/kinds.go`: kinds, their Terraform types/blocks and Crossplane GVKs, pinned upstream versions
- `internal/grafana/provider.go`: check, discover, org scoping, import IDs
- `internal/grafana/oss.go`, `alerting.go`: one reader per kind
- `testdata/api` (recorded answers), `testdata/upstream` (Terraform schema, CRDs)

## Rules that come from real runs

- A mapping change is done when the live test passes: first plan imports only
  (plus in-place updates of secrets Grafana redacts), apply, second plan has
  no changes; Crossplane objects Ready and Synced without changing Grafana.
- Every sensitive attribute in the Terraform schema goes through `sdk.Secret`
  (the upstream-schema test enforces it).
- Never export part of an object whose missing part Terraform would delete;
  skip the whole object with a reason.
- Grafana's list endpoints omit fields (data sources: basicAuthUser,
  secureJsonFields): read each object.
- New organizations get their Alertmanager configuration asynchronously
  (about a minute); alerting calls fail with 500 until then.

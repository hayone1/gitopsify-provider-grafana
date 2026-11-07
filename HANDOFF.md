# Handoff: gitopsify-provider-grafana

State as of 2026-11-07. github.com/hayone1/gitopsify-provider-grafana
(private). Built from the same layout as
github.com/hayone1/gitopsify-provider-template: `tools/upstream` and
`tools/record` are copies of the template's (only the import differs; keep
them in sync).

## What it is
The first gitopsify provider: `gitopsify-provider-grafana` reads a Grafana over
its HTTP API and describes it as grafana/grafana Terraform resources (pinned
`~> 4.53`), from which gitopsify writes Terraform and Crossplane
(provider-grafana v2.17, namespaced `.m.` APIs). Design and protocol:
`../gitopsify/docs/DESIGN-providers.md`, `../gitopsify/docs/writing-providers.md`.

## Where things are
- `main.go` (sdk.Serve), `internal/grafana/`: `kinds.go` (kinds, Terraform
  types, blocks, IgnoreChanges, Crossplane GVKs, pinned versions),
  `provider.go` (check, discover, org scoping and membership, import IDs
  `<org>:<id>`, warnings per org × kind), `client.go` (HTTP, `user:pass` vs
  token, retries + Retry-After, TLS/ca_cert, record mode), `oss.go`,
  `alerting.go` (one reader per kind).
- `testdata/api/` recorded answers (`make record` against a seeded Grafana),
  `testdata/upstream/` trimmed Terraform schema and CRDs (`make upstream`,
  tools/upstream: a copy of gitopsify-provider-template's).
- `test/live/seed.sh` (3 orgs; API-made and UI-made alerting objects, team
  preferences, a v2 dashboard), `test/live/crossplane.sh` (live test).

## Verified (2026-11-07)
- `make lint test`: conformance; discover details (org scoping, refs, skips,
  secrets, summary, token auth, provisioned and v2 dashboards, provenance of
  API- and UI-made objects, depth-5 policies, unsupported team preferences);
  every resource validated against the Terraform schema and the CRDs.
- Local, Grafana 13.3.0 + Terraform 1.14.6, every kind incl. Organization:
  first plan imports only + in-place updates of 2 contact points per org whose
  secrets Grafana redacts (checked field by field); apply; second plan "No
  changes" in all 3 workspaces. Service-account token: same, no org_id. A
  re-export of the Terraform-managed Grafana changed nothing.
- Live on Hetzner (`make live`, SERVER_TYPE=cpx32), 2026-11-07: PASS.
  Terraform 1.14.6 and OpenTofu 1.13.9 strict round trips with every kind;
  Crossplane 2.4.3 + provider-grafana v2.17.0: 49 managed resources Ready and
  Synced observing and managing; Grafana unchanged throughout.
- Three rounds of fresh-context review; the last one found nothing blocking.

## Open / next
1. Tag v0.1.0 once go.mod requires a tagged SDK (no replace directive); add
   the repository's `repoId` to `../gitopsify/providers/index.yaml`. Make the
   repository public first (installs need it).
   Known: `fixtureName` uses url.PathEscape, which keeps `:` (invalid on
   Windows and in Go module zips); no current fixture has one. Switch to the
   template's `FixtureName` (and rename testdata/api) when re-recording.
   `tools/record` is untested against a real Grafana (the template's copy is
   tested); the next `make record` is its first real run.
2. Contact points with settings the provider doesn't map are skipped whole:
   map more integrations (pagerduty, teams, discord, opsgenie, telegram...),
   and put string-valued unknown settings into Terraform's sensitive
   `settings` map (Crossplane `settingsSecretRef`) instead of skipping.
3. Grafana 13 dashboards stored with the new layouts (v2) are skipped:
   support them through `grafana_apps_dashboard_*`.
4. Organizations the server admin isn't a member of are skipped whole; the
   Organization resource itself could still be read (admin APIs).
5. Summary mode (the wizard's picker) lists provisioned and v2 dashboards the
   full read then skips.
6. `muteTimingRef` in Crossplane policies (the CRD's singular field holds a
   list): only matters for creation order when replicating.
7. A Crossplane run with a service-account token (Terraform was checked).

## Grafana behaviour that shapes the mapping
- List endpoints leave fields out (data sources: `basicAuthUser`,
  `secureJsonFields`): read each object.
- `/api/orgs` counts pages from 0; `/api/search` from 1.
- Secret fields are never returned (data sources) or returned only to admins
  through `/api/v1/provisioning/contact-points/export?decrypt=true`; the
  Terraform provider can't read contact point secrets back, so adopting them
  shows one in-place update with the same values.
- Terraform's contact point update writes defaults (maxAlerts 0) into Grafana:
  the provider treats false/0 settings as unset so exports stay stable.
- `disable_provenance` is ForceNew on mute timings and never read back:
  always false there, and every alerting kind ignores it (Terraform
  lifecycle; Crossplane initProvider).
- The rule group endpoint's rules carry no provenance: use the flat list's.
- A new organization's Alertmanager is set up asynchronously (~1 minute).
- `org_id` is only allowed with basic auth; tokens are org-scoped; import IDs
  are `<org>:<id>` either way (Terraform parses a numeric prefix as the org).
- `grafana_organization` member lists are authoritative and the admin is read
  as a member: write every member, `admin_user = ""`, `create_users = false`.
- Terraform's validators: team preferences timezone ∈ {utc, browser}, theme ∈
  {light, dark, system}, week start ∈ {sunday, monday, saturday};
  `json_data_encoded` refuses `teamHttpHeaders`; the deepest (5th) policy
  level requires `group_by`.
- Grafana 13 converts v2 dashboards to v1 on the classic endpoint;
  `/apis/dashboard.grafana.app/v1beta1/namespaces/<ns>/dashboards/<uid>`
  `status.conversion.storedVersion` tells them apart (namespace from
  `/api/frontend/settings`).

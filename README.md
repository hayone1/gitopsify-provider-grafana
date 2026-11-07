# gitopsify-provider-grafana

The [gitopsify](https://github.com/hayone1/gitopsify) provider for **Grafana**
(OSS and Enterprise): it reads what a Grafana holds, so `gitopsify export`
can write it as Terraform/OpenTofu or Crossplane configuration that manages
the same objects. One workspace per organization; the first plan imports
everything and changes nothing.

```sh
gitopsify provider install grafana
export GRAFANA_URL=https://grafana.example.com
export GRAFANA_AUTH=admin:...            # a server admin, to export every organization
gitopsify export grafana                 # asks what to export; --no-input for CI
```

See [gitopsify's guide](https://github.com/hayone1/gitopsify/blob/main/docs/providers.md)
for the output, adoption, secrets, CI and re-exports.

## What it reads

| Kind | Terraform | Crossplane | Notes |
|---|---|---|---|
| Organization | `grafana_organization` | `Organization` | Optional. Needs server-admin basic auth. Member lists become authoritative. The main organization is never exported |
| Folder | `grafana_folder` | `Folder` | Nested folders reference their parent |
| Dashboard | `grafana_dashboard` | `Dashboard` | JSON in files (`dashboards/<folder path>/<title>.json`); provisioned dashboards are skipped |
| LibraryPanel | `grafana_library_panel` | `LibraryPanel` | |
| DataSource | `grafana_data_source` | `DataSource` | Secret fields can't be read back: left out and reported; read-only (provisioned) ones are skipped |
| Team | `grafana_team` | `Team` | Not members |
| Playlist | `grafana_playlist` | `Playlist` | |
| MessageTemplate | `grafana_message_template` | `MessageTemplate` | |
| MuteTiming | `grafana_mute_timing` | `MuteTiming` | |
| ContactPoint | `grafana_contact_point` | `ContactPoint` | Email, Slack and webhook integrations; contact points with other integrations are skipped whole. Secret settings are read for admins |
| NotificationPolicy | `grafana_notification_policy` | `NotificationPolicy` | Up to 4 levels of nesting |
| RuleGroup | `grafana_rule_group` | `RuleGroup` | |

Objects provisioned from files are skipped (with the reason in the report):
they are managed where they are provisioned.

Output targets [grafana/grafana](https://registry.terraform.io/providers/grafana/grafana)
`~> 4.53` and [provider-grafana](https://github.com/grafana/crossplane-provider-grafana)
v2.17 (Crossplane 2, namespaced managed resources). Tested against Grafana
13.3.

## Credentials

| Setting | Environment | |
|---|---|---|
| `url` | `GRAFANA_URL` | Root URL of the Grafana |
| `auth` | `GRAFANA_AUTH` | `user:password` of a server admin (every organization), or a service account token (its organization only; the output then has no `org_id`) |
| `insecure_skip_verify` | `GRAFANA_INSECURE_SKIP_VERIFY` | `true` to skip TLS verification |
| `ca_cert` | `GRAFANA_CA_CERT` | PEM file of CAs to trust |

The variables are the Terraform provider's, so the same environment runs
`gitopsify export` and `terraform plan`.

## Development

```sh
make build test lint
make install                              # on PATH, where gitopsify finds it
make upstream                             # refresh testdata/upstream after bumping the pinned upstream versions
make live                                 # Grafana + kind + Crossplane + Terraform + OpenTofu on a throwaway Hetzner machine
```

Unit tests replay answers recorded from a real Grafana (`testdata/`) and
validate every resource against the Terraform schema and the Crossplane CRDs.
Until gitopsify's SDK is tagged, this repository builds against a `../gitopsify`
checkout.

## License

[Apache 2.0](LICENSE)

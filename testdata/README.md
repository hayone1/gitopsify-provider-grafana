# Test data

- `api/`: answers recorded from a Grafana filled by `test/live/seed.sh`, replayed
  by the unit tests. Re-record after changing what the provider reads:

  ```sh
  docker run -d --name grafana -p 3300:3000 -e GF_SECURITY_ADMIN_PASSWORD=admin grafana/grafana:13.3.0
  GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin test/live/seed.sh
  GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin make record
  ```

  Only record against that throwaway Grafana: recordings contain the secret
  values it returns (decrypted contact point settings).

- `upstream/terraform-schema.json`: `terraform providers schema -json` for
  grafana/grafana at the version in `internal/grafana/kinds.go`, trimmed to the
  resource types the provider writes. `make upstream` refreshes it; `upstream/VERSIONS` says which versions it came from.
- `upstream/crds/`: the CRDs of crossplane provider-grafana at the version in
  `kinds.go`, from the package's `package.yaml`.

The unit tests validate every resource against both (`sdktest.ValidateTerraform`,
`sdktest.ValidateCrossplane`), so a mapping mistake shows up without running
Terraform or Crossplane.

// Command gitopsify-provider-grafana is the gitopsify provider for Grafana:
// it reads organizations, folders, dashboards, data sources, alerting and
// more, so "gitopsify export grafana" can write them as Terraform or
// Crossplane configuration.
package main

import (
	"github.com/hayone1/gitopsify-provider-grafana/internal/grafana"
	"github.com/hayone1/gitopsify/sdk"
)

// version is set at build time (-ldflags "-X main.version=v0.1.0").
var version = "dev"

func main() { sdk.Serve(grafana.New(version)) }

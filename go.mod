module github.com/hayone1/gitopsify-provider-grafana

go 1.23

require (
	github.com/hayone1/gitopsify/sdk v0.0.0-00010101000000-000000000000
	sigs.k8s.io/yaml v1.6.0
)

require go.yaml.in/yaml/v2 v2.4.2 // indirect

// Until github.com/hayone1/gitopsify/sdk has a tagged release, build against
// the sibling checkout. Remove this line and require a sdk/vX.Y.Z tag before
// releasing (go install refuses modules with replace directives).
replace github.com/hayone1/gitopsify/sdk => ../gitopsify/sdk

package grafana

import "github.com/hayone1/gitopsify/sdk"

// Upstream providers the output is written for and tested against.
const (
	terraformSource  = "grafana/grafana"
	terraformVersion = "~> 4.53"
	crossplanePkg    = "xpkg.upbound.io/grafana/provider-grafana:v2.17.0"
)

// Kind names.
const (
	kOrganization       = "Organization"
	kFolder             = "Folder"
	kDashboard          = "Dashboard"
	kLibraryPanel       = "LibraryPanel"
	kDataSource         = "DataSource"
	kTeam               = "Team"
	kPlaylist           = "Playlist"
	kMessageTemplate    = "MessageTemplate"
	kMuteTiming         = "MuteTiming"
	kContactPoint       = "ContactPoint"
	kNotificationPolicy = "NotificationPolicy"
	kRuleGroup          = "RuleGroup"
)

func oss(kind string) *sdk.CrossplaneType {
	return &sdk.CrossplaneType{APIVersion: "oss.grafana.m.crossplane.io/v1alpha1", Kind: kind}
}

func alerting(kind string) *sdk.CrossplaneType {
	return &sdk.CrossplaneType{APIVersion: "alerting.grafana.m.crossplane.io/v1alpha1", Kind: kind}
}

func tf(typ string, blocks ...string) *sdk.TerraformType {
	return &sdk.TerraformType{Type: typ, Blocks: blocks}
}

// alertingTF is an alerting type: the Terraform provider can't read
// disable_provenance back after an import (and replaces mute timings over
// it), so Terraform ignores it.
func alertingTF(typ string, blocks ...string) *sdk.TerraformType {
	return &sdk.TerraformType{Type: typ, Blocks: blocks, IgnoreChanges: []string{"disable_provenance"}}
}

// maxPolicyDepth is how deep the Terraform schema nests policy blocks.
const maxPolicyDepth = 5

// policyBlocks lists the nested notification policy blocks the Terraform
// schema allows.
func policyBlocks() []string {
	var out []string
	p := "policy"
	for i := 0; i < maxPolicyDepth; i++ {
		out = append(out, p, p+".matcher")
		p += ".policy"
	}
	return out
}

var kinds = []sdk.Kind{
	{Name: kOrganization, Plural: "Organizations", Optional: true,
		Description: "organizations and their members (needs server-admin basic auth; the member lists become authoritative)",
		Terraform:   tf("grafana_organization"), Crossplane: oss("Organization")},
	{Name: kFolder, Plural: "Folders", Description: "folders, nested ones included",
		Terraform: tf("grafana_folder"), Crossplane: oss("Folder")},
	{Name: kDashboard, Plural: "Dashboards", Description: "dashboards (their JSON goes into files)",
		Terraform: tf("grafana_dashboard"), Crossplane: oss("Dashboard")},
	{Name: kLibraryPanel, Plural: "LibraryPanels", Description: "library panels",
		Terraform: tf("grafana_library_panel"), Crossplane: oss("LibraryPanel")},
	{Name: kDataSource, Plural: "DataSources", Description: "data sources (secret fields can't be read back)",
		Terraform: tf("grafana_data_source"), Crossplane: oss("DataSource")},
	{Name: kTeam, Plural: "Teams", Description: "teams (not their members)",
		Terraform: tf("grafana_team", "preferences", "team_sync"), Crossplane: oss("Team")},
	{Name: kPlaylist, Plural: "Playlists", Description: "playlists",
		Terraform: tf("grafana_playlist", "item"), Crossplane: oss("Playlist")},
	{Name: kMessageTemplate, Plural: "MessageTemplates", Description: "alerting notification templates",
		Terraform: alertingTF("grafana_message_template"), Crossplane: alerting("MessageTemplate")},
	{Name: kMuteTiming, Plural: "MuteTimings", Description: "alerting mute timings",
		Terraform: alertingTF("grafana_mute_timing", "intervals", "intervals.times"), Crossplane: alerting("MuteTiming")},
	{Name: kContactPoint, Plural: "ContactPoints", Description: "alerting contact points (email, Slack and webhook integrations)",
		Terraform: alertingTF("grafana_contact_point", "email", "slack", "webhook"), Crossplane: alerting("ContactPoint")},
	{Name: kNotificationPolicy, Plural: "NotificationPolicies", Description: "the alerting notification policy tree of each organization",
		Terraform: alertingTF("grafana_notification_policy", policyBlocks()...), Crossplane: alerting("NotificationPolicy")},
	{Name: kRuleGroup, Plural: "RuleGroups", Description: "alert rule groups",
		Terraform:  alertingTF("grafana_rule_group", "rule", "rule.data", "rule.data.relative_time_range", "rule.notification_settings", "rule.record"),
		Crossplane: alerting("RuleGroup")},
}

func description(version string) sdk.Description {
	return sdk.Description{
		Name:        "grafana",
		Title:       "Grafana",
		Version:     version,
		Description: "Reads a Grafana (OSS or Enterprise) through its HTTP API.",
		Homepage:    "https://github.com/hayone1/gitopsify-provider-grafana",
		Settings: []sdk.Setting{
			{Name: "url", Title: "Grafana URL", Description: "the root URL of the Grafana server", Env: "GRAFANA_URL",
				Required: true, Example: "https://grafana.example.com", Terraform: "url", Crossplane: "url"},
			{Name: "auth", Title: "Credentials", Description: "user:password of a server admin (to export every organization), or a service account token (one organization)",
				Env: "GRAFANA_AUTH", Secret: true, Required: true, Terraform: "auth", Crossplane: "auth"},
			{Name: "insecure_skip_verify", Title: "Skip TLS verification", Description: "true to accept any TLS certificate (testing only)",
				Env: "GRAFANA_INSECURE_SKIP_VERIFY", Terraform: "insecure_skip_verify"},
			{Name: "ca_cert", Title: "CA certificate", Description: "certificate authorities to trust: a PEM file's path or the PEM text",
				Env: "GRAFANA_CA_CERT", Terraform: "ca_cert"},
		},
		Partition: sdk.Noun{Singular: "organization", Plural: "organizations",
			Description: "Grafana organizations; each becomes its own workspace"},
		Kinds:     kinds,
		Terraform: &sdk.TerraformProvider{Name: "grafana", Source: terraformSource, Version: terraformVersion},
		Crossplane: &sdk.CrossplaneProvider{Name: "provider-grafana", Package: crossplanePkg,
			ProviderConfig: sdk.GVK{APIVersion: "grafana.m.crossplane.io/v1beta1", Kind: "ClusterProviderConfig"}},
	}
}

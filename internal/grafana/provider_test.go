package grafana

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hayone1/gitopsify/sdk"
	"github.com/hayone1/gitopsify/sdk/sdktest"
	"sigs.k8s.io/yaml"
)

// fakeGrafana replays the answers recorded from the Grafana that
// test/live/seed.sh fills (see testdata/README.md). overrides replace
// answers by "<org> <path>".
func fakeGrafana(t *testing.T, overrides map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org := int64(0)
		if h := r.Header.Get("X-Grafana-Org-Id"); h != "" {
			org, _ = strconv.ParseInt(h, 10, 64)
		}
		if body, ok := overrides[strconv.FormatInt(org, 10)+" "+r.URL.RequestURI()]; ok {
			if strings.HasPrefix(body, "HTTP ") {
				code, _ := strconv.Atoi(strings.Fields(body)[1])
				http.Error(w, `{"message":"`+body+`"}`, code)
				return
			}
			_, _ = w.Write([]byte(body))
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "api", fixtureName(org, r.URL.RequestURI())))
		if err != nil {
			http.Error(w, `{"message":"no fixture for `+r.URL.RequestURI()+`"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func request(srv *httptest.Server) sdk.Request {
	return sdk.Request{Settings: map[string]string{"url": srv.URL, "auth": "admin:admin"}}
}

func TestConformance(t *testing.T) {
	sdktest.Conformance(t, New("test"), request(fakeGrafana(t, nil)))
}

func TestDiscover(t *testing.T) {
	inv := sdktest.Discover(t, New("test"), request(fakeGrafana(t, nil)))
	byKind := map[string]int{}
	byKey := map[string]sdk.Resource{}
	for _, r := range inv.Resources {
		byKind[r.Kind]++
		byKey[r.Key] = r
	}
	if len(inv.Partitions) != 3 {
		t.Errorf("partitions: %+v", inv.Partitions)
	}
	if byKind[kDashboard] != 7 || byKind[kFolder] != 7 || byKind[kContactPoint] != 8 || byKind[kRuleGroup] != 4 {
		t.Errorf("counts: %v", byKind)
	}

	dash := byKey["dashboard/2/k8s-overview"]
	if dash.ImportID != "2:k8s-overview" {
		t.Errorf("import ID %q", dash.ImportID)
	}
	org, ok := dash.Attributes["org_id"].(sdk.RefValue)
	if !ok || org.Value != "2" || org.Crossplane != "organizationRef" {
		t.Errorf("org_id = %#v", dash.Attributes["org_id"])
	}
	file, ok := dash.Attributes["config_json"].(sdk.FileValue)
	if !ok || file.File != "dashboards/SRE/Kubernetes/Kubernetes / Overview.json" {
		t.Errorf("config_json = %#v", dash.Attributes["config_json"])
	}
	text, _ := sdk.FileText(file.Content)
	if strings.Contains(string(text), `"id"`) && strings.Index(string(text), `"id"`) < 10 || strings.Contains(string(text), `"version":`) {
		t.Errorf("dashboard keeps id/version:\n%.300s", text)
	}
	if !strings.Contains(string(text), "<b>Namespaces</b> & pods in ${ns}") {
		t.Errorf("dashboard text changed:\n%s", text)
	}

	slack := byKey["contactpoint/1/ops-slack"].Attributes["slack"].([]any)[0].(map[string]any)
	if s, ok := slack["url"].(sdk.SecretValue); !ok || s.Value != "https://hooks.slack.com/services/T000/B000/XXXX" || s.Crossplane != "urlSecretRef" {
		t.Errorf("slack url = %#v", slack["url"])
	}
	email := byKey["contactpoint/1/ops-email"].Attributes["email"].([]any)[0].(map[string]any)
	if strings.Join(toStrings(email["addresses"]), ",") != "ops@example.com,sre@example.com" || email["single_email"] != true {
		t.Errorf("email = %#v", email)
	}
	ds := byKey["datasource/1/prom"].Attributes
	if s, ok := ds["secure_json_data_encoded"].(sdk.SecretValue); !ok || s.Value != nil {
		t.Errorf("secure json = %#v", ds["secure_json_data_encoded"])
	}
	loki := byKey["datasource/1/loki"].Attributes
	if _, ok := loki["http_headers"].(sdk.SecretValue); !ok {
		t.Errorf("loki headers = %#v", loki["http_headers"])
	}
	if j, ok := loki["json_data_encoded"].(sdk.JSONValue); !ok || strings.Contains(string(j.JSON.(sdk.RawJSON)), "httpHeaderName1") {
		t.Errorf("loki json data = %#v", loki["json_data_encoded"])
	}
	policy := byKey["notificationpolicy/1"].Attributes
	inner := policy["policy"].([]any)[0].(map[string]any)["policy"].([]any)[0].(map[string]any)
	if ref, ok := inner["contact_point"].(sdk.RefValue); !ok || ref.Ref != "contactpoint/1/pager" {
		t.Errorf("nested policy = %#v", inner)
	}
	skipped := 0
	for _, s := range inv.Skipped {
		if s.Kind == kContactPoint && s.Name == "chat" && strings.Contains(s.Reason, "discord") {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("skipped: %+v", inv.Skipped)
	}
	if _, ok := byKey[orgKey(1)]; ok {
		t.Error("the main organization was exported")
	}

	// A dashboard stored with the new layouts (v2) is skipped.
	v2 := false
	for _, s := range inv.Skipped {
		v2 = v2 || (s.Name == "New layouts" && strings.Contains(s.Reason, "v2"))
	}
	if !v2 {
		t.Errorf("v2 dashboard not skipped: %+v", inv.Skipped)
	}
	// UI-made objects: no provenance.
	if mt := byKey["mutetiming/1/maintenance"].Attributes; mt["disable_provenance"] != false {
		t.Errorf("mute timing disable_provenance = %v (must stay false: changing it replaces the mute timing)", mt["disable_provenance"])
	}
	uiEmail := byKey["contactpoint/1/ui-email"].Attributes["email"].([]any)[0].(map[string]any)
	if strings.Join(toStrings(uiEmail["addresses"]), "|") != "team@example.com| lead@example.com" {
		t.Errorf("addresses must be split like Terraform does (no trimming): %q", uiEmail["addresses"])
	}
	// Team preferences.
	team := byKey["team/1/1"].Attributes
	prefs, ok := team["preferences"].([]any)
	if !ok || prefs[0].(map[string]any)["theme"] != "dark" {
		t.Errorf("team preferences = %#v", team["preferences"])
	} else if ref, ok := prefs[0].(map[string]any)["home_dashboard_uid"].(sdk.RefValue); !ok || ref.Ref != "dashboard/1/home" {
		t.Errorf("home dashboard = %#v", prefs[0].(map[string]any)["home_dashboard_uid"])
	}
	// Organizations list every member, the admin included, with admin_user "".
	org2 := byKey[orgKey(2)].Attributes
	if org2["admin_user"] != "" || org2["create_users"] != false || !strings.Contains(strings.Join(toStrings(org2["admins"]), ","), "admin@localhost") {
		t.Errorf("organization = %#v", org2)
	}
	if byKey["folder/1/sre"].ImportID != "1:sre" {
		t.Errorf("import ID %q", byKey["folder/1/sre"].ImportID)
	}
}

func TestTokenAuthHasNoOrgID(t *testing.T) {
	srv := fakeGrafana(t, map[string]string{"0 /api/org": `{"id":2,"name":"Team A"}`})
	// Without basic auth no org header is sent, so answer org 0 with org 2's
	// recordings.
	srv.Config.Handler = rewriteOrg(srv.Config.Handler, "2")
	req := sdk.Request{Settings: map[string]string{"url": srv.URL, "auth": "glsa_token"}, Scope: sdk.Scope{Kinds: []string{kFolder, kDashboard}}}
	inv := sdktest.Discover(t, New("test"), req)
	if len(inv.Partitions) != 1 || inv.Partitions[0].ID != "2" {
		t.Fatalf("partitions %+v", inv.Partitions)
	}
	for _, r := range inv.Resources {
		if _, ok := r.Attributes["org_id"]; ok {
			t.Errorf("%s has org_id with a token", r.Key)
		}
		// The Terraform provider reads "<org>:<id>" in either mode.
		if !strings.HasPrefix(r.ImportID, "2:") {
			t.Errorf("%s import ID %q has no org", r.Key, r.ImportID)
		}
	}
}

func rewriteOrg(h http.Handler, org string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/org" && r.URL.Path != "/api/health" {
			r.Header.Set("X-Grafana-Org-Id", org)
		}
		h.ServeHTTP(w, r)
	})
}

func TestProvisionedAndFailures(t *testing.T) {
	srv := fakeGrafana(t, map[string]string{
		"1 /api/dashboards/uid/home":            `{"dashboard":{"uid":"home","title":"Home"},"meta":{"provisioned":true}}`,
		"2 /api/v1/provisioning/alert-rules":    "HTTP 500",
		"3 /api/v1/provisioning/contact-points": "HTTP 403",
	})
	inv := sdktest.Discover(t, New("test"), request(srv))
	found := false
	for _, s := range inv.Skipped {
		found = found || (s.Name == "Home" && strings.Contains(s.Reason, "provisioned"))
	}
	if !found {
		t.Errorf("provisioned dashboard not skipped: %+v", inv.Skipped)
	}
	want := map[string]bool{"2/" + kRuleGroup: false, "3/" + kContactPoint: false}
	for _, w := range inv.Warnings {
		k := w.Partition + "/" + w.Kind
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("no warning for %s: %+v", k, inv.Warnings)
		}
	}
}

func TestSummary(t *testing.T) {
	req := request(fakeGrafana(t, nil))
	req.Detail = sdk.DetailSummary
	req.Scope.Kinds = []string{kDashboard}
	inv := sdktest.Discover(t, New("test"), req)
	for _, r := range inv.Resources {
		if r.Attributes != nil {
			t.Errorf("%s has attributes in a summary", r.Key)
		}
		if r.Key == "dashboard/1/k8s-overview" && (len(r.DependsOn) != 1 || r.DependsOn[0] != "folder/1/sre-k8s") {
			t.Errorf("depends on %v", r.DependsOn)
		}
	}
}

func TestCheckErrors(t *testing.T) {
	srv := fakeGrafana(t, map[string]string{"0 /api/user": "HTTP 401"})
	_, err := sdktest.CheckErr(t, New("test"), request(srv))
	if err == nil || !strings.Contains(err.Error(), "GRAFANA_AUTH") {
		t.Fatalf("err = %v", err)
	}
	_, err = sdktest.CheckErr(t, New("test"), sdk.Request{Settings: map[string]string{"url": "ftp://x"}})
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("err = %v", err)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

// TestUpstreamSchemas checks the whole inventory against the Terraform
// provider's schema and the Crossplane CRDs the output targets.
func TestUpstreamSchemas(t *testing.T) {
	d := sdktest.Describe(t, New("test"))
	inv := sdktest.Discover(t, New("test"), request(fakeGrafana(t, nil)))
	schema, err := os.ReadFile("../../testdata/upstream/terraform-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sdktest.ValidateTerraform(schema, d, inv) {
		t.Errorf("terraform: %s", p)
	}
	files, _ := filepath.Glob("../../testdata/upstream/crds/*.yaml")
	var crds []map[string]any
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var crd map[string]any
		if err := yaml.Unmarshal(b, &crd); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		crds = append(crds, crd)
	}
	for _, p := range sdktest.ValidateCrossplane(crds, d, inv) {
		t.Errorf("crossplane: %s", p)
	}
	// Every block the schema has for these types is declared.
	for _, k := range d.Kinds {
		want, err := sdktest.TerraformBlocks(schema, terraformSource, k.Terraform.Type)
		if err != nil {
			t.Fatal(err)
		}
		declared := map[string]bool{}
		for _, b := range k.Terraform.Blocks {
			declared[b] = true
		}
		for _, b := range want {
			used := false
			for _, r := range inv.Resources {
				if r.Kind == k.Name && usesPath(r.Attributes, b) {
					used = true
				}
			}
			if used && !declared[b] {
				t.Errorf("kind %s writes block %s without declaring it", k.Name, b)
			}
		}
	}
}

func usesPath(attrs map[string]any, path string) bool {
	head, rest, more := strings.Cut(path, ".")
	v, ok := attrs[head]
	if !ok {
		return false
	}
	if !more {
		return true
	}
	switch t := v.(type) {
	case map[string]any:
		return usesPath(t, rest)
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok && usesPath(m, rest) {
				return true
			}
		}
	}
	return false
}

func TestRuleGroupProvenanceAndUIObjects(t *testing.T) {
	inv := sdktest.Discover(t, New("test"), request(fakeGrafana(t, nil)))
	byKey := map[string]sdk.Resource{}
	for _, r := range inv.Resources {
		byKey[r.Key] = r
	}
	// The group endpoint has no provenance; the flat list's counts.
	if v := byKey["rulegroup/1/sre-k8s/kubernetes"].Attributes["disable_provenance"]; v != false {
		t.Errorf("API-made rule group: disable_provenance = %v", v)
	}
	if v := byKey["rulegroup/1/apps/ui-made"].Attributes["disable_provenance"]; v != true {
		t.Errorf("UI-made rule group: disable_provenance = %v", v)
	}
}

func TestDeepPolicyTree(t *testing.T) {
	route := func(receiver string, sub string) string {
		return `{"receiver":"` + receiver + `","object_matchers":[["a","=","b"]]` + sub + `}`
	}
	tree := route("ops-email", `,"routes":[`+route("ops-email", `,"routes":[`+route("ops-email", `,"routes":[`+route("ops-email", `,"routes":[`+route("ops-email", `,"routes":[`+route("pager", "")+`]`)+`]`)+`]`)+`]`)+`]`)
	tree = tree[:len(tree)-1] + `,"group_by":["alertname"]}`
	srv := fakeGrafana(t, map[string]string{"1 /api/v1/provisioning/policies": tree})
	req := request(srv)
	req.Scope.Kinds = []string{kNotificationPolicy}
	req.Scope.Partitions = []string{"1"}
	inv := sdktest.Discover(t, New("test"), req)
	if len(inv.Resources) != 1 {
		t.Fatalf("resources %+v, skipped %+v", inv.Resources, inv.Skipped)
	}
	p := inv.Resources[0].Attributes
	for level := 1; level <= maxPolicyDepth; level++ {
		p = p["policy"].([]any)[0].(map[string]any)
	}
	if gb, ok := p["group_by"].([]any); !ok || len(gb) != 0 {
		t.Errorf("level %d group_by = %#v, want []", maxPolicyDepth, p["group_by"])
	}
	if ref, ok := p["contact_point"].(sdk.RefValue); !ok || ref.Crossplane != "" {
		t.Errorf("level %d contact_point = %#v, want a ref without Crossplane field", maxPolicyDepth, p["contact_point"])
	}
}

func TestTeamPreferencesTerraformRefuses(t *testing.T) {
	srv := fakeGrafana(t, map[string]string{"1 /api/teams/1/preferences": `{"theme":"dark","timezone":"Europe/Berlin"}`})
	req := request(srv)
	req.Scope.Kinds = []string{kTeam}
	inv := sdktest.Discover(t, New("test"), req)
	for _, r := range inv.Resources {
		if r.Key == "team/1/1" {
			t.Error("team with an unsupported timezone was exported")
		}
	}
	found := false
	for _, s := range inv.Skipped {
		found = found || (s.Kind == kTeam && strings.Contains(s.Reason, "Europe/Berlin"))
	}
	if !found {
		t.Errorf("skipped: %+v", inv.Skipped)
	}
}

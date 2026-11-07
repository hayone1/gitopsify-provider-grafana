#!/usr/bin/env bash
# Fills a fresh Grafana with what people build by hand: three organizations,
# nested folders, dashboards, library panels, data sources with secrets,
# teams, playlists and alerting (templates, mute timings, contact points, a
# nested notification policy, rule groups). Used by run.sh.
#
#   GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin ./seed.sh
set -euo pipefail

url=${GRAFANA_URL:?set GRAFANA_URL}
auth=${GRAFANA_AUTH:?set GRAFANA_AUTH (user:password of a server admin)}

api() { # api ORG METHOD PATH [BODY [HEADER]]
  local org=$1 method=$2 path=$3 body=${4:-} header=${5:-X-Seed: 1} out code
  out=$(curl -sS -w '\n%{http_code}' -u "$auth" -H 'Content-Type: application/json' -H "X-Grafana-Org-Id: $org" -H "$header" \
    -X "$method" "$url$path" ${body:+-d "$body"})
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [[ $code != 2* ]]; then
    echo "$method $path (org $org): HTTP $code: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

org_id() { api 1 GET "/api/orgs/name/$(jq -rn --arg n "$1" '$n|@uri')" | jq -r .id; }

for name in "Team A" "Platform"; do
  api 1 POST /api/orgs "$(jq -n --arg n "$name" '{name: $n}')" >/dev/null 2>&1 || true
done
team_a=$(org_id "Team A")
platform=$(org_id "Platform")
echo "organizations: 1, $team_a, $platform"

# A user who is an editor of Team A.
api 1 POST /api/admin/users '{"name":"Ana","email":"ana@example.com","login":"ana","password":"ana-password-1"}' >/dev/null 2>&1 || true
api "$team_a" POST /api/org/users '{"loginOrEmail":"ana","role":"Editor"}' >/dev/null 2>&1 || true

seed_org() {
  local o=$1
  # Folders, nested.
  api "$o" POST /api/folders '{"uid":"sre","title":"SRE"}' >/dev/null
  api "$o" POST /api/folders '{"uid":"sre-k8s","title":"Kubernetes","parentUid":"sre"}' >/dev/null
  api "$o" POST /api/folders '{"uid":"apps","title":"Apps & Services"}' >/dev/null

  # Data sources: one with basic auth and a secret, one plain, one with a header.
  api "$o" POST /api/datasources '{"uid":"prom","name":"Prometheus","type":"prometheus","url":"http://prometheus:9090","access":"proxy","isDefault":true,
    "basicAuth":true,"basicAuthUser":"grafana","secureJsonData":{"basicAuthPassword":"s3cret"},
    "jsonData":{"httpMethod":"POST","timeInterval":"30s"}}' >/dev/null
  api "$o" POST /api/datasources '{"uid":"loki","name":"Loki","type":"loki","url":"http://loki:3100","access":"proxy",
    "jsonData":{"maxLines":1000,"httpHeaderName1":"X-Scope-OrgID"},"secureJsonData":{"httpHeaderValue1":"tenant-1"}}' >/dev/null

  # Dashboards: template variables, HTML, a library panel.
  api "$o" POST /api/library-elements '{"uid":"lp-cpu","name":"CPU usage","kind":1,"folderUid":"sre",
    "model":{"type":"timeseries","title":"CPU usage","datasource":{"type":"prometheus","uid":"prom"},
      "targets":[{"refId":"A","expr":"sum(rate(container_cpu_usage_seconds_total[5m]))"}]}}' >/dev/null
  api "$o" POST /api/dashboards/db '{"folderUid":"sre-k8s","dashboard":{"uid":"k8s-overview","title":"Kubernetes / Overview","tags":["k8s"],
    "templating":{"list":[{"name":"ds","type":"datasource","query":"prometheus"},{"name":"ns","type":"query","datasource":{"uid":"${ds}"},"query":"label_values(namespace)"}]},
    "panels":[{"id":1,"type":"text","title":"Read me","gridPos":{"x":0,"y":0,"w":12,"h":4},"options":{"mode":"html","content":"<b>Namespaces</b> & pods in ${ns}"}},
      {"id":2,"gridPos":{"x":12,"y":0,"w":12,"h":8},"libraryPanel":{"uid":"lp-cpu","name":"CPU usage"}}]}}' >/dev/null
  api "$o" POST /api/dashboards/db '{"dashboard":{"uid":"home","title":"Home","panels":[]}}' >/dev/null
  api "$o" POST /api/dashboards/db '{"folderUid":"apps","dashboard":{"uid":"checkout","title":"Checkout / Latency","refresh":"30s",
    "panels":[{"id":1,"type":"stat","title":"p99","targets":[{"refId":"A","expr":"histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{job=\"checkout\"}[5m]))"}]}]}}' >/dev/null

  # A team with preferences, and a playlist.
  team=$(api "$o" POST /api/teams '{"name":"On-call","email":"oncall@example.com"}' | jq -r .teamId)
  api "$o" PUT "/api/teams/$team/preferences" '{"theme":"dark","homeDashboardUID":"home","timezone":"utc"}' >/dev/null
  api "$o" POST /api/playlists '{"name":"Wallboard","interval":"5m","items":[{"type":"dashboard_by_uid","value":"k8s-overview"},{"type":"dashboard_by_tag","value":"k8s"}]}' >/dev/null

  # Alerting. Grafana sets up a new organization's Alertmanager in the
  # background (up to a minute): wait for it.
  for _ in $(seq 1 60); do
    api "$o" GET /api/v1/provisioning/policies >/dev/null 2>&1 && break
    sleep 2
  done
  api "$o" PUT /api/v1/provisioning/templates/slack.title '{"template":"{{ define \"slack.title\" }}[{{ .Status | toUpper }}] {{ .CommonLabels.alertname }}{{ end }}"}' >/dev/null
  api "$o" POST /api/v1/provisioning/mute-timings '{"name":"weekends","time_intervals":[{"weekdays":["saturday","sunday"]},{"times":[{"start_time":"22:00","end_time":"23:59"}],"location":"Europe/Berlin"}]}' >/dev/null
  api "$o" POST /api/v1/provisioning/contact-points '{"name":"ops-email","type":"email","settings":{"addresses":"ops@example.com;sre@example.com","singleEmail":true}}' >/dev/null
  api "$o" POST /api/v1/provisioning/contact-points '{"name":"ops-slack","type":"slack","settings":{"url":"https://hooks.slack.com/services/T000/B000/XXXX","recipient":"#alerts","title":"{{ template \"slack.title\" . }}"}}' >/dev/null
  api "$o" POST /api/v1/provisioning/contact-points '{"name":"pager","type":"webhook","settings":{"url":"https://pager.example.com/hook","httpMethod":"POST","username":"grafana","password":"hook-secret"}}' >/dev/null
  api "$o" POST /api/v1/provisioning/contact-points '{"name":"chat","type":"discord","settings":{"url":"https://discord.com/api/webhooks/1/x"}}' >/dev/null
  # Objects as the UI makes them: without provenance.
  ui='X-Disable-Provenance: true'
  api "$o" POST /api/v1/provisioning/mute-timings '{"name":"maintenance","time_intervals":[{"times":[{"start_time":"02:00","end_time":"04:00"}],"weekdays":["tuesday"]}]}' "$ui" >/dev/null
  api "$o" POST /api/v1/provisioning/contact-points '{"name":"ui-email","type":"email","settings":{"addresses":"team@example.com, lead@example.com"}}' "$ui" >/dev/null
  api "$o" PUT /api/v1/provisioning/templates/ui.footer '{"template":"{{ define \"ui.footer\" }}Sent by Grafana{{ end }}"}' "$ui" >/dev/null
  api "$o" POST /api/v1/provisioning/alert-rules '{"title":"Disk almost full","ruleGroup":"ui-made","folderUID":"apps","condition":"B","for":"10m","noDataState":"OK","execErrState":"Alerting",
    "data":[{"refId":"A","relativeTimeRange":{"from":300,"to":0},"datasourceUid":"prom","model":{"refId":"A","expr":"node_filesystem_avail_bytes"}},
      {"refId":"B","relativeTimeRange":{"from":0,"to":0},"datasourceUid":"__expr__","model":{"refId":"B","type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"lt","params":[1000000000]}}]}}]}' "$ui" >/dev/null
  api "$o" PUT /api/v1/provisioning/policies '{"receiver":"ops-email","group_by":["grafana_folder","alertname"],"group_wait":"30s",
    "routes":[{"receiver":"ops-slack","object_matchers":[["severity","=","critical"]],"continue":true,
      "routes":[{"receiver":"pager","object_matchers":[["team","=~","sre|platform"]],"mute_time_intervals":["weekends"]}]},
      {"receiver":"ops-email","object_matchers":[["severity","!=","critical"]],"group_by":["alertname"]}]}' >/dev/null
  api "$o" POST /api/v1/provisioning/alert-rules '{"title":"High CPU","ruleGroup":"kubernetes","folderUID":"sre-k8s","condition":"C","for":"5m",
    "noDataState":"NoData","execErrState":"Error","labels":{"severity":"critical","team":"sre"},"annotations":{"summary":"CPU above 90% in {{ $labels.namespace }}"},
    "data":[{"refId":"A","relativeTimeRange":{"from":600,"to":0},"datasourceUid":"prom","model":{"refId":"A","expr":"sum by (namespace) (rate(container_cpu_usage_seconds_total[5m]))"}},
      {"refId":"C","relativeTimeRange":{"from":0,"to":0},"datasourceUid":"__expr__","model":{"refId":"C","type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[0.9]}}]}}]}' >/dev/null
  api "$o" PUT /api/v1/provisioning/folder/sre-k8s/rule-groups/kubernetes '{"title":"kubernetes","interval":60,"rules":'"$(api "$o" GET /api/v1/provisioning/alert-rules | jq -c '[.[] | select(.ruleGroup=="kubernetes")]')"'}' >/dev/null
}

seed_org 1
seed_org "$team_a"
# A dashboard with Grafana 13's new layouts (stored as v2): skipped.
api 1 POST /apis/dashboard.grafana.app/v2beta1/namespaces/default/dashboards '{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"v2-layouts"},"spec":{"title":"New layouts","elements":{},"layout":{"kind":"GridLayout","spec":{"items":[]}},"cursorSync":"Off","preload":false,"liveNow":false,"editable":true,"tags":[],"links":[],"annotations":[],"variables":[],"timeSettings":{"timezone":"browser","from":"now-6h","to":"now","autoRefresh":"","autoRefreshIntervals":["5s"],"hideTimepicker":false,"fiscalYearStartMonth":0}}}' >/dev/null
# Platform only has a folder and a dashboard.
api "$platform" POST /api/folders '{"uid":"infra","title":"Infrastructure"}' >/dev/null
api "$platform" POST /api/dashboards/db '{"folderUid":"infra","dashboard":{"uid":"nodes","title":"Nodes","panels":[]}}' >/dev/null
echo "seeded $url"

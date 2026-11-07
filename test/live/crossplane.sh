#!/usr/bin/env bash
# Live test of gitopsify export with this provider, on a machine with Docker,
# kind, kubectl, helm, Go, terraform and tofu (gitopsify's
# test/live/hetzner/e2e-on-vm.sh prepares one):
#
#   cd ../gitopsify && UPLOAD=../gitopsify-provider-grafana \
#     RUN=/gitopsify-provider-grafana/test/live/crossplane.sh ./test/live/hetzner/e2e-on-vm.sh
#
# 1. Grafana in a kind cluster, filled by seed.sh.
# 2. Crossplane and provider-grafana; export to Crossplane; apply with the
#    observe-only component: every managed resource must become Ready and
#    Synced; then without it: still Ready and Synced, and Grafana unchanged.
# 3. Export to Terraform; with terraform and with tofu: plan (imports and the
#    expected in-place updates of redacted secrets only), apply, plan again
#    (no changes), and Grafana unchanged.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
provider=$(cd "$here/../.." && pwd)
gitopsify=${GITOPSIFY_SRC:-$(cd "$provider/../gitopsify" && pwd)}
work=${WORK:-/tmp/grafana-live}
crossplane_version=${CROSSPLANE_VERSION:-2.4.3}
grafana_image=${GRAFANA_IMAGE:-grafana/grafana:13.3.0}
rm -rf "$work" && mkdir -p "$work/bin"
export PATH="$work/bin:$PATH"

log() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*"; exit 1; }

log "building gitopsify and the provider"
(cd "$gitopsify" && CGO_ENABLED=0 go build -o "$work/bin/gitopsify" ./cmd/gitopsify)
(cd "$provider" && CGO_ENABLED=0 go build -o "$work/bin/gitopsify-provider-grafana" .)

log "kind cluster with Grafana"
kind delete cluster --name grafana-live >/dev/null 2>&1 || true
kind create cluster --name grafana-live --wait 120s
kubectl create namespace grafana
# One rollout, with the environment from the start (a second rollout would
# leave the port-forward attached to a terminating pod).
kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: grafana}
spec:
  selector: {matchLabels: {app: grafana}}
  template:
    metadata: {labels: {app: grafana}}
    spec:
      containers:
        - name: grafana
          image: $grafana_image
          env: [{name: GF_SECURITY_ADMIN_PASSWORD, value: admin}]
          ports: [{containerPort: 3000}]
          readinessProbe: {httpGet: {path: /api/health, port: 3000}}
---
apiVersion: v1
kind: Service
metadata: {name: grafana, namespace: grafana}
spec:
  selector: {app: grafana}
  ports: [{port: 3000}]
YAML
kubectl -n grafana rollout status deployment/grafana --timeout=600s
# Keep a port-forward to Grafana for the whole test, restarting it if it dies.
(while :; do kubectl -n grafana port-forward svc/grafana 3300:3000 >>"$work/port-forward.log" 2>&1; sleep 1; done) &
pf=$!
trap 'kill $pf 2>/dev/null || true; pkill -P $pf 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do curl -fsS localhost:3300/api/health >/dev/null 2>&1 && break; sleep 2; done
curl -fsS localhost:3300/api/health >/dev/null || { cat "$work/port-forward.log"; fail "Grafana isn't reachable through the port-forward"; }
export GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin
"$here/seed.sh"

snapshot() { # snapshot FILE: what the provider reads (and skips), normalized
  echo '{"settings":{"url":"http://localhost:3300","auth":"admin:admin"}}' |
    gitopsify-provider-grafana discover 2>/dev/null |
    jq -S -c 'select(.resource or .skipped) | (.resource // .skipped) | del(.notes)' | sort >"$1"
}
snapshot "$work/before.json"
echo "$(wc -l <"$work/before.json") objects in Grafana"
cd "$work"
# Every kind, the optional ones included.
kinds=Organization,Folder,Dashboard,LibraryPanel,DataSource,Team,Playlist,MessageTemplate,MuteTiming,ContactPoint,NotificationPolicy,RuleGroup

# A first plan may import everything and update in place only contact points
# (Grafana doesn't give Terraform their secret settings back): nothing is
# created, replaced or deleted, and nothing else changes.
check_plan() { # check_plan CLI PLANFILE NAME
  local bad
  # Secret fields (and their settings map) differ; nothing else may.
  bad=$($1 show -json "$2" | jq -r 'def strip: walk(if type == "object" then
      del(.url, .token, .basic_auth_password, .authorization_credentials, .settings, .uid) |
      with_entries(select(.value != null and .value != {} and .value != [] and .value != "" and .value != 0 and .value != false)) else . end);
    .resource_changes[]? | select(.change.actions != ["no-op"] and .change.actions != ["read"]) |
    select(.change.actions != ["update"] or .type != "grafana_contact_point" or ((.change.before | strip) != (.change.after | strip))) |
    "\(.address): \(.change.actions | join(","))"')
  if [[ -n $bad ]]; then
    echo "$bad"
    $1 show -no-color "$2" | sed -n '1,150p' 
    fail "$1: unexpected changes in the first plan of $3"
  fi
}

for cli in terraform tofu; do
  command -v $cli >/dev/null || { echo "skipping $cli: not installed"; continue; }
  log "export to Terraform, round trip with $cli"
  rm -rf "$work/tf-$cli"
  gitopsify export grafana --no-input --to terraform --kind "$kinds" -o "$work/tf-$cli" --secret-values local
  for ws in "$work/tf-$cli"/terraform/default/*/; do
    name=$(basename "$ws")
    (cd "$ws"
      $cli init -input=false -no-color >init.log 2>&1 || { tail -20 init.log; fail "$cli init $name"; }
      $cli plan -input=false -no-color -out=plan.bin >plan1.log 2>&1 || { tail -40 plan1.log; fail "$cli plan $name"; }
      echo "$name: $(grep -E '^Plan:' plan1.log || echo 'no changes')"
      check_plan $cli plan.bin "$name"
      $cli apply -input=false -no-color -auto-approve plan.bin >apply.log 2>&1 || { tail -40 apply.log; fail "$cli apply $name"; }
      $cli plan -input=false -no-color -detailed-exitcode >plan2.log 2>&1 || { cat plan2.log; fail "$cli: $name changes after apply"; })
  done
  # tofu starts from fresh state: it imports again what terraform managed.
  snapshot "$work/after-$cli.json"
  diff -u "$work/before.json" "$work/after-$cli.json" || fail "$cli changed Grafana"
done
echo "Terraform and OpenTofu adopted everything without changing Grafana"
snapshot "$work/before-crossplane.json"

log "Crossplane $crossplane_version"
helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null
helm install crossplane crossplane-stable/crossplane --version "$crossplane_version" \
  --namespace crossplane-system --create-namespace --wait --timeout 10m >/dev/null

log "export to Crossplane"
gitopsify export grafana --no-input --to crossplane --kind "$kinds" -o out --secret-values local
kubectl apply -k out/crossplane/setup
kubectl wait provider.pkg.crossplane.io/provider-grafana --for=condition=Healthy --timeout=15m
# Healthy can come before every CRD is served.
kubectl wait crd --all --for=condition=Established --timeout=5m
# The provider pods reach Grafana through its Service, not the port-forward
# (the credentials gitopsify wrote under .secrets/ name the port-forward).
kubectl -n crossplane-system create secret generic grafana-default-credentials \
  --from-literal=credentials='{"url":"http://grafana.grafana.svc:3000","auth":"admin:admin"}'

wait_managed() { # wait_managed PHASE: every managed resource Ready and Synced
  local deadline=$((SECONDS + 900)) total bad
  while :; do
    total=$(kubectl get managed -A -o json | jq '.items | length')
    bad=$(kubectl get managed -A -o json | jq -r '.items[] | select(([.status.conditions[]? | select((.type=="Ready" or .type=="Synced") and .status=="True")] | length) < 2) | "\(.kind)/\(.metadata.namespace)/\(.metadata.name)"')
    if [[ $total -gt 0 && -z $bad ]]; then
      echo "$1: $total managed resources Ready and Synced"
      return 0
    fi
    if ((SECONDS > deadline)); then
      echo "$1: not Ready/Synced after 15 minutes:"
      for r in $bad; do
        kind=${r%%/*}; rest=${r#*/}; ns=${rest%%/*}; name=${rest#*/}
        kubectl -n "$ns" get "$kind" "$name" -o json | jq -c '{name: .metadata.name, conditions: [.status.conditions[]? | {type, status, reason, message}]}'
      done
      return 1
    fi
    sleep 10
  done
}

log "apply, observe only"
kubectl apply --server-side -k out/crossplane || { sleep 30; kubectl apply --server-side -k out/crossplane; }
# Secrets of the managed resources (Slack URLs, webhook passwords), now that
# their namespaces exist.
if [[ -d out/.secrets/crossplane/default ]]; then
  kubectl apply --server-side -R -f out/.secrets/crossplane/default/
fi
wait_managed "observe-only" || fail "managed resources didn't become Ready while observing"
policies=$(kubectl get managed -A -o json | jq -r '[.items[].spec.managementPolicies | join(",")] | unique | join(" ")')
[[ $policies == "Observe" ]] || fail "observe-only component didn't apply: $policies"
snapshot "$work/observed.json"
diff -u "$work/before-crossplane.json" "$work/observed.json" || fail "observing changed Grafana"

log "apply, managing"
sed -i '/components:/,+1d' out/crossplane/kustomization.yaml
kubectl apply --server-side -k out/crossplane
sleep 60 # let every resource reconcile with the new policies at least once
wait_managed "managing" || fail "managed resources didn't stay Ready once managing"
snapshot "$work/managed.json"
diff -u "$work/before-crossplane.json" "$work/managed.json" || fail "managing changed Grafana"
echo "Crossplane adopted everything without changing Grafana"

log "PASS"

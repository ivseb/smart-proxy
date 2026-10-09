#!/usr/bin/env bash
# End-to-end tests on a local kind cluster: builds Smart Proxy and a test application, installs
# them with the Helm chart and runs test/e2e. Needs docker, kind, kubectl and helm.
#
#   test/e2e/run.sh            # create the cluster, test, delete the cluster
#   KEEP=1 test/e2e/run.sh     # keep the cluster afterwards (re-runs reuse it)
set -euo pipefail
cd "$(dirname "$0")/../.."

CLUSTER=${CLUSTER:-smart-proxy-e2e}
export KUBECONFIG=${KUBECONFIG_E2E:-$(mktemp -d)/kubeconfig}
WORK=$(mktemp -d)

cleanup() {
  if [ "${KEEP:-}" != 1 ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config test/e2e/kind.yaml --wait 120s
else
  kind export kubeconfig --name "$CLUSTER"
fi

echo "--- Building images"
docker build -q -t smart-proxy:e2e . >/dev/null
CGO_ENABLED=0 GOOS=linux GOARCH="$(docker version -f '{{.Server.Arch}}')" go build -o "$WORK/echoapp" ./test/e2e/echoapp
docker build -q -t smart-proxy-e2e-echo:latest -f test/e2e/echoapp/Dockerfile "$WORK" >/dev/null
kind load docker-image smart-proxy:e2e smart-proxy-e2e-echo:latest --name "$CLUSTER"

echo "--- Installing"
kubectl apply -f test/e2e/app.yaml
kubectl -n e2e rollout restart deployment/echo >/dev/null 2>&1 || true
kubectl -n e2e rollout status deployment/echo --timeout=120s
helm upgrade --install e2e charts/smart-proxy -n smart-proxy --create-namespace -f test/e2e/values.yaml --wait --timeout 180s
kubectl -n smart-proxy rollout restart deployment/e2e-smart-proxy >/dev/null
kubectl -n smart-proxy rollout status deployment/e2e-smart-proxy --timeout=180s
kubectl -n smart-proxy patch service e2e-smart-proxy --type=json -p '[
  {"op":"replace","path":"/spec/type","value":"NodePort"},
  {"op":"add","path":"/spec/ports/0/nodePort","value":30080},
  {"op":"add","path":"/spec/ports/1/nodePort","value":30081}]' >/dev/null

echo "--- Testing"
if ! go test -tags e2e -count=1 -timeout 30m -v ./test/e2e/ "$@"; then
  echo "--- Smart Proxy logs"
  kubectl -n smart-proxy logs -l app.kubernetes.io/name=smart-proxy --tail=300 --prefix || true
  exit 1
fi

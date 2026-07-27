#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kustomize_version="${KUSTOMIZE_VERSION:-v5.7.1}"
kubeconform_version="${KUBECONFORM_VERSION:-v0.6.7}"
go_binary="${GO_BINARY:-go}"
release_renderer="$repo_root/scripts/render-kubernetes-release.sh"
projection_repair_job="$repo_root/deploy/kubernetes/operations/projection-repair-job.example.yaml"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

render() {
  local source_dir="$1"
  local output_file="$2"
  GOWORK=off "$go_binary" run "sigs.k8s.io/kustomize/kustomize/v5@${kustomize_version}" build "$source_dir" >"$output_file"
}

render "$repo_root/deploy/kubernetes/base" "$tmp_dir/core.yaml"
render "$repo_root/deploy/kubernetes/overlays/search" "$tmp_dir/search.yaml"
release_image="registry.example.invalid/live-auction/backend:2026.07.27@sha256:1111111111111111111111111111111111111111111111111111111111111111"
AUCTION_RELEASE_IMAGE="$release_image" GO_BINARY="$go_binary" KUSTOMIZE_VERSION="$kustomize_version" \
  "$release_renderer" core >"$tmp_dir/release-core.yaml"
AUCTION_RELEASE_IMAGE="$release_image" GO_BINARY="$go_binary" KUSTOMIZE_VERSION="$kustomize_version" \
  "$release_renderer" search >"$tmp_dir/release-search.yaml"
GOWORK=off "$go_binary" run "github.com/yannh/kubeconform/cmd/kubeconform@${kubeconform_version}" \
  -strict -summary \
  "$tmp_dir/core.yaml" "$tmp_dir/search.yaml" \
  "$tmp_dir/release-core.yaml" "$tmp_dir/release-search.yaml" \
  "$projection_repair_job"

zero_image="live-auction-bid-backend:release-template@sha256:0000000000000000000000000000000000000000000000000000000000000000"
test "$(rg -c --fixed-strings "image: $zero_image" "$tmp_dir/core.yaml")" -eq 8
test "$(rg -c --fixed-strings "image: $zero_image" "$tmp_dir/search.yaml")" -eq 11
test "$(rg -c --fixed-strings "image: $release_image" "$tmp_dir/release-core.yaml")" -eq 8
test "$(rg -c --fixed-strings "image: $release_image" "$tmp_dir/release-search.yaml")" -eq 11
if AUCTION_RELEASE_IMAGE=registry.example.invalid/live-auction/backend:mutable "$release_renderer" core >/dev/null 2>&1; then
  echo "release renderer accepted a mutable image tag" >&2
  exit 1
fi
if AUCTION_RELEASE_IMAGE="$zero_image" "$release_renderer" core >/dev/null 2>&1; then
  echo "release renderer accepted the source-template zero digest" >&2
  exit 1
fi

require_rendered_name() {
  local output_file="$1"
  local resource_name="$2"
  if ! rg -q -- "^  name: ${resource_name}$" "$output_file"; then
    echo "rendered manifest is missing resource ${resource_name}: ${output_file}" >&2
    exit 1
  fi
}

require_rendered_kind() {
  local output_file="$1"
  local resource_kind="$2"
  local resource_name="$3"
  if ! awk -v expected_kind="$resource_kind" -v expected_name="$resource_name" '
      /^---$/ { kind = "" }
      /^kind: / { kind = $2 }
      $0 == "  name: " expected_name && kind == expected_kind { found = 1 }
      END { exit(found ? 0 : 1) }
    ' "$output_file"; then
    echo "rendered manifest is missing ${resource_kind}/${resource_name}: ${output_file}" >&2
    exit 1
  fi
}

for name in auction-gateway auction-service outbox-relay projector domain-relay enrichment-consumer close-worker auction-migrate; do
  require_rendered_name "$tmp_dir/core.yaml" "$name"
done
require_rendered_kind "$tmp_dir/core.yaml" Deployment auction-service
require_rendered_kind "$tmp_dir/release-core.yaml" Deployment auction-service
rg -q -- '^  AUCTION_PROJECTION_GATE_ENABLED: "true"$' "$repo_root/deploy/kubernetes/base/runtime-config.yaml"
rg -q -- 'name: AUCTION_KAFKA_CLIENT_PROPERTIES_FILE' "$tmp_dir/core.yaml"
rg -q -- 'secretName: auction-kafka-auction-service' "$tmp_dir/core.yaml"
rg -q -- 'name: auction-kafka-auction-service' "$repo_root/deploy/kubernetes/secrets/external-secrets.example.yaml"
rg -q -- 'property: auction_service_client_properties' "$repo_root/deploy/kubernetes/secrets/external-secrets.example.yaml"
for name in index-es index-pgvector search-reconciler; do
  require_rendered_name "$tmp_dir/search.yaml" "$name"
  if rg -q -- "^  name: ${name}$" "$tmp_dir/core.yaml"; then
    echo "optional search resource leaked into the core base: ${name}" >&2
    exit 1
  fi
done

if rg -n -- '^kind:[[:space:]]+Secret$' "$repo_root/deploy/kubernetes"; then
  echo "Kubernetes source contains a directly managed Secret" >&2
  exit 1
fi
if rg -n -- 'image: .*:latest([[:space:]]|$)|image: .*:release([[:space:]]|$)' "$repo_root/deploy/kubernetes"; then
  echo "Kubernetes source contains a mutable application image tag" >&2
  exit 1
fi

for rendered in "$tmp_dir/core.yaml" "$tmp_dir/search.yaml"; do
  rg -q '^kind: HorizontalPodAutoscaler$' "$rendered"
  rg -q '^kind: PodDisruptionBudget$' "$rendered"
  rg -q '^kind: NetworkPolicy$' "$rendered"
  rg -q 'readOnlyRootFilesystem: true' "$rendered"
  rg -q 'topology.kubernetes.io/zone' "$rendered"
done

echo "Kustomize core and search manifests satisfy the workload contract."

# Kubernetes deployment contract

This directory contains the production workload shape from the refactoring blueprint. Stateful infrastructure is intentionally external: Kafka, MySQL, Redis/Sentinel, NATS, Elasticsearch and PostgreSQL/pgvector must be managed Multi-AZ services or installed by the platform repository.

## Layout

- `base/`: gateway, auction-service, Relay, Projector, Domain Relay, Enrichment, close-worker, migration Job, Services, PDBs, HPA and NetworkPolicies.
- `overlays/search/`: adds index-es, index-pgvector and search-reconciler, then configures the Gateway hybrid retrievers.
- `operations/`: non-Kustomized one-shot Job examples. They are never part of an application rollout and must be edited and reviewed per incident.
- `secrets/`: External Secrets contract only; it never contains credentials.

## Required platform inputs

1. Three-zone Kubernetes cluster with Metrics Server and a CNI enforcing NetworkPolicy.
2. Prometheus adapter exposing `auction_ws_connections_total_per_pod` for the Gateway HPA. CPU can still scale up when that metric is unavailable, but scale-down is deliberately blocked until every configured metric is healthy.
3. The observability namespace labelled `live-auction.io/observability-access=true`.
4. External Secrets Operator and the environment-owned `ClusterSecretStore` referenced by `secrets/external-secrets.example.yaml`.
5. Managed endpoints and per-component Kafka principals described in `docs/infra/target-infrastructure-runbook.md`.

## Mandatory environment patching

The checked-in base is a contract, not a directly deployable production environment. Application images deliberately use an all-zero digest sentinel so that applying the source template fails closed instead of pulling a mutable tag. Before rendering an environment overlay:

- provide `AUCTION_RELEASE_IMAGE=repository:immutable-version@sha256:<64-hex>` to the release renderer; digest-less, unversioned, uppercase and all-zero references are rejected;
- replace all Redis Sentinel DNS names and `AUCTION_REDIS_MASTER_NAME`;
- replace `AUCTION_WS_ALLOWED_ORIGINS=https://auction.example.invalid`;
- confirm pool budgets against the environment's MySQL `max_connections`;
- provide the ExternalSecret remote records and Kafka `client.properties` files;
- choose the core base or the search overlay explicitly.

The renderer applies the equivalent Kustomize image transform:

```yaml
images:
  - name: live-auction-bid-backend
    newName: registry.example.com/live-auction/backend
    digest: sha256:RELEASE_DIGEST
```

Never commit a rendered Secret or a real credential.

## Render and deploy

```bash
bash scripts/check-kubernetes-manifests.sh

# Core auction path
AUCTION_RELEASE_IMAGE=registry.example.com/live-auction/backend:RELEASE_VERSION@sha256:RELEASE_DIGEST \
  bash scripts/render-kubernetes-release.sh core > rendered-core.yaml

# Core plus hybrid search
AUCTION_RELEASE_IMAGE=registry.example.com/live-auction/backend:RELEASE_VERSION@sha256:RELEASE_DIGEST \
  bash scripts/render-kubernetes-release.sh search > rendered-search.yaml
```

Only the rendered output is deployable. The manifest gate validates both source templates and rendered release manifests, requires every image reference to end in a nonzero SHA-256 digest, and rejects the old mutable release tag.

Apply the environment-owned secret resources first and wait for every ExternalSecret to be Ready. The migration Job is an Argo CD `PreSync` hook; non-Argo release pipelines must run the same image with `/app/auction-migrate up` before rolling workloads.

## Rollout rules

- Gateway uses a 10-minute HPA scale-down stabilization window because each removed Pod migrates WebSocket clients.
- `auction-service` is a stateless Deployment. Any replica may execute any command; runtime correctness and serialization come from Redis Lua, never from Pod identity or static room ownership.
- Relay replica count must never exceed the fixed 16 outbox shards. Fencing and leases determine ownership.
- Projector replica count must not exceed the Runtime Topic partition count. Configure KEDA outside this base only when its maximum obeys that bound.
- Projector gap recovery uses the one-shot `auction-projection-repair` binary and the reviewed procedure in `docs/PROJECTOR_GAP_RECOVERY_RUNBOOK.md`; both the original-replay Job and the read-only-evidence synthetic Job are preview-only until their arguments are deliberately changed under dual control.
- Search consumers scale independently and may be omitted without changing the auction core path.
- Readiness checks safety dependencies; liveness only checks process health. Unexpected Runner exit terminates the process instead of leaving a false-positive operations endpoint.

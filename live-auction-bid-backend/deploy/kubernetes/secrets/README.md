# Target secret contract

`external-secrets.example.yaml` declares the names and keys consumed by target workloads. It contains no secret value and is deliberately not part of an automatically applied base because every environment must provide its own `ClusterSecretStore`.

Before applying it in a shared environment:

1. install and pin External Secrets Operator;
2. create `ClusterSecretStore/production-secret-store` through the platform repository;
3. create the referenced remote records and grant the controller read access only to the `live-auction/<environment>/` prefix;
4. replace the example namespace and remote-key prefix through the environment overlay;
5. apply and wait for every `ExternalSecret` to report `Ready=True` before starting a workload;
6. mount `client.properties` files read-only with mode `0400`; inject scalar credentials through `secretKeyRef`.

Never add a `data:` or `stringData:` Kubernetes Secret containing a real credential to this repository. Local-only credentials remain scoped to the disposable Compose projects.

Kafka principals stay separate even if the external provider stores them under one administrative record. This preserves least-privilege ACLs and allows independent rotation without restarting unrelated consumers.

| Workload | Kubernetes Secret | Kafka scope |
|---|---|---|
| `auction-service` | `auction-kafka-auction-service` | Direct-partition `Read` and `Describe` on `auction.runtime.projection.v1`; no group or write ACL |
| `outbox-relay` | `auction-kafka-outbox-relay` | Runtime Topic producer |
| `projector` | `auction-kafka-projector` | Runtime Topic consumer group |
| `projection-repair` | `auction-kafka-projection-repair` | Audited direct-partition Runtime Topic reader |

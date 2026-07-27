# Three-node failure lab

This Compose project is a disposable correctness lab for the three stateful systems that must be exercised under failover:

- three combined-role Kafka 4.2 KRaft nodes, RF=3 and min ISR=2;
- one Redis primary, two replicas and three Sentinels;
- three Core NATS nodes with JetStream disabled and application subject ACLs.

It is not a production deployment and its `*-test-only` credentials must never be reused. A single-host run catches protocol, quorum, fencing and reconnect defects; the same services must be placed on three independent test machines or availability zones before treating a failover result as infrastructure evidence.

```bash
docker compose -p live-auction-ha -f deploy/ha/docker-compose.yml up -d
docker compose -p live-auction-ha -f deploy/ha/docker-compose.yml ps
docker compose -p live-auction-ha -f deploy/ha/docker-compose.yml down
```

Do not add `-v` to `down` unless the explicit goal is to destroy the lab evidence and rebuild all state.

Expected experiments are documented in [`docs/infra/target-infrastructure-runbook.md`](../../docs/infra/target-infrastructure-runbook.md).

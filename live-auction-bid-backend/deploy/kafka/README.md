# Kafka infrastructure contract

Local and CI use isolated plaintext KRaft listeners. Shared test and production environments must use SASL_SSL with SCRAM-SHA-512 (or cloud IAM), one principal per deployment unit, and a Secret/CSI-mounted client properties file.

Initialize topics and ACLs with an admin credential:

```bash
KAFKA_BOOTSTRAP_SERVERS=kafka-1:9092,kafka-2:9092,kafka-3:9092 \
KAFKA_REPLICATION_FACTOR=3 \
KAFKA_MIN_INSYNC_REPLICAS=2 \
KAFKA_COMMAND_CONFIG=/var/run/secrets/kafka/admin.properties \
scripts/kafka-init-topics.sh

KAFKA_BOOTSTRAP_SERVERS=kafka-1:9092,kafka-2:9092,kafka-3:9092 \
KAFKA_COMMAND_CONFIG=/var/run/secrets/kafka/admin.properties \
scripts/kafka-apply-acls.sh
```

The scripts are idempotent. Topic initialization refuses an existing partition count above the contract because Kafka cannot shrink partitions; it only increases an undersized topic. Runtime Topic retention is 90 days and all target topics disable compaction and automatic creation.

The checked-in [`client.properties.example`](./client.properties.example) contains placeholders only. Rendered credentials, truststores and keystores must remain outside Git, container images and ordinary ConfigMaps.

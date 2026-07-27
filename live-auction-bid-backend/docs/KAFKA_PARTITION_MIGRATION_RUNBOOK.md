# Kafka 分区迁移 Runbook

Kafka keyed Topic 的分区数不是普通容量参数。增加分区会改变默认 key 路由，同一 `lot_id` 或 `order_id` 的旧、新事件可能进入不同分区；Kafka 又无法缩减分区。因此 `scripts/kafka-init-topics.sh` 只创建 Topic、校验固定分区数和修正安全的 Topic 配置，禁止对既有 Runtime Topic 直接执行 `--alter --partitions`。

## 首选方案：版本化 Topic

对 `auction.runtime.projection.v1` 优先新建 `v2`，不要在线修改 `v1`：

1. 先部署能同时读取 v1/v2 的 Projector，并为 v2 建立独立的 DB partition offset。
2. 固定一条持久化路由：迁移开始后只有新 lot 写 v2，已在 v1 出现的 lot 终身写 v1，禁止按当前分区数重新计算旧 lot。
3. Relay produce 前根据持久化路由选择 Topic；事件的 `event_id`、`lot_version` 和 payload hash 语义不变。
4. 验证 v1/v2 各自无 gap、Projector inbox 全局幂等、领域事件无重复副作用。
5. 等 v1 所有 lot 终态、Redis pending/inflight 清零、Kafka lag 为零且保留期结束后，再退役 v1。

该方案允许新旧版本并存于迁移期，但不是两套业务链路：Lua、Redis Outbox、Relay 与 Projector 事务模型保持唯一，只增加显式的不可变路由版本。

## 备选方案：全量排空窗口

只有同时满足以下条件，才允许对现有 Topic 增加分区：

- 禁止新开拍，且 Redis 中没有 LIVE/EXTENDED lot；
- 16 个 Redis Outbox pending/inflight 全部为零；
- Runtime Projector 的每个 Kafka partition lag 为零，DB next offset 等于 high watermark；
- Domain Outbox 已发布完成，相关领域消费者 lag 为零；
- 没有 paused partition、P0/P1 reconcile finding 或未完成 repair；
- 变更经过双人复核，并归档上述水位、Topic describe、consumer group 和数据库查询结果。

窗口内停止 Relay 和所有生产者，记录旧分区数，执行一次显式变更，再更新仓库声明、Topic 契约测试、监控和容量基线。恢复顺序为 Projector/消费者 → Relay → auction-service 新开拍，恢复后新建专用 lot 验证完整版本链。

## 失败处理

Kafka 增加分区后无法回滚为缩容。若验证失败：

1. 立即停止新开拍和相关生产者，不删除旧数据、不重置 consumer offset。
2. 保留现场的 Topic、DB offset、inbox、Redis Outbox 和日志证据。
3. 若存在同 key 跨分区，切换到版本化 Topic 与持久化路由方案；不得依赖手工 offset 跳过、强制覆盖 MySQL 或重新哈希活跃 lot。
4. 只有逐 lot 核对 `last_event_id + version + canonical_hash` 后才能恢复。

## 验收证据

证据包至少包含变更审批、前后 `kafka-topics --describe`、所有 consumer group lag、DB partition offset、Redis pending/inflight、活跃 lot 数、reconcile finding、专用 lot 的完整 event/version 链，以及恢复或失败处置时间线。

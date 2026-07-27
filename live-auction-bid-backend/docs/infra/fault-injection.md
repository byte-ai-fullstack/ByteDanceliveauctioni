# 故障注入门禁

`scripts/run-fault-injection.sh` 负责精确定位 Compose 服务、注入 pause/SIGKILL、恢复服务并归档证据。容器重新进入 `healthy` 只证明进程恢复，不证明竞拍数据正确，因此每次真实注入都必须提供业务断言程序。

## 执行

只解析目标、不修改容器：

```bash
DRY_RUN=1 ./scripts/run-fault-injection.sh relay-kill
```

真实注入：

```bash
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT=/absolute/path/to/assert-runtime-recovery \
FAULT_DURATION_SECONDS=10 \
./scripts/run-fault-injection.sh relay-kill
```

Projector pause/kill has a repository-owned assertion implementation:

```bash
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$PWD/scripts/assert-projector-recovery.mjs" \
FAULT_ASSERT_AFTER_SERVICE_START=1 \
FAULT_DURATION_SECONDS=40 \
./scripts/run-fault-injection.sh projector-pause
```

It creates dedicated merchant/buyer/lot data, records every gate snapshot and
queries the Compose MySQL service after recovery. Run it only in an isolated
fault environment; unrelated Runtime Topic traffic intentionally invalidates
the zero-new-fact proof for an `OVERLOADED` command.

`FAULT_ASSERT_AFTER_SERVICE_START=1` is intended for recovery assertions that
must observe intermediate application state. The runner first proves the
container process is `running`, executes `after`, and only then accepts Docker
`healthy`; the assertion remains timeout-bounded and a process-only recovery
can never produce `PASS`.

Relay pause/kill also has a repository-owned assertion:

```bash
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$PWD/scripts/assert-relay-recovery.mjs" \
FAULT_DURATION_SECONDS=20 \
./scripts/run-fault-injection.sh relay-kill
```

The assertion starts with a drained Redis Outbox and projected baseline, accepts
alternating bids while Relay is unavailable, and proves those facts accumulate
only in the 16 Redis `pending/inflight` queues. After recovery it requires all
queues to drain, all 16 fenced owners to exist, ownership epochs to advance for
`relay-kill`, successful ACKs to cover every injected fact, invariant ACK
outcomes to remain zero, and the MySQL inbox/lot projection version chain to be
gap-free. It persists no Redis password, API token or account password.

断言程序必须是绝对路径、普通可执行文件。每个阶段默认最多运行 120 秒，可通过 `FAULT_ASSERTION_TIMEOUT_SECONDS` 调整；服务恢复等待默认 90 秒，可通过 `FAULT_RECOVERY_TIMEOUT_SECONDS` 调整。所有超时都有上限，避免故障状态无限挂起。

## 断言契约

同一个断言程序依次收到三次调用：

```text
assert-runtime-recovery <phase> <scenario> <result_dir> <service> <container_id>
```

同时提供等价环境变量：

```text
FAULT_PHASE
FAULT_SCENARIO
FAULT_SERVICE
FAULT_CONTAINER_ID
FAULT_RESULT_DIR
FAULT_COMPOSE_FILE
```

阶段语义：

| 阶段 | 服务状态 | 断言职责 |
|---|---|---|
| `before` | 正常 | 记录业务水位、版本、offset、finding 数和基线指标 |
| `during` | 已 pause/kill | 制造确定的竞拍事件并证明故障窗口确实出现，不能只等待时间 |
| `after` | 服务恢复且健康 | 等待追平，并比较前后业务水位与唯一性不变量；不满足时返回非零 |

断言程序在任一阶段失败，故障脚本立即返回失败；若服务仍处于 pause/kill 状态，退出 trap 会先执行恢复。脚本不会提供跳过业务断言的成功开关。

## 各场景最低断言

| 场景 | 必须证明 |
|---|---|
| `relay-*` | 故障期间 Redis Outbox 形成积压；接管后 pending/inflight 清空；按 `event_id` 去重后 `lot_version` 连续，ACK mismatch/malformed 为零 |
| `projector-*` | Kafka lag 在故障期间增长；达到阈值后开拍/出价/直播配置同步返回 `OVERLOADED` 且零新增事实，取消和到期落槌仍可用；恢复后 DB offset 连续追平，闸门连续 3 次健康采样后开启；inbox、业务投影、domain outbox 同事务，没有新 P0/P1 finding |
| `domain-*` | 未发布 domain outbox 增长后归零；重复投递不产生重复订单、补全或索引副作用 |
| `redis-*` | 写入冻结与 generation 变化可见；核对完成前 readiness 不恢复；已确认语义符合耐久性声明 |
| `mysql-*` | Runtime 裁决不被改成 MySQL 直写；Projector 重放后 offset 与业务版本一致，没有部分事务 |
| `kafka-*` | 生产失败不 ACK Redis inflight；Broker 恢复后事件完整且 Projector 无 gap |
| `gateway-*` | 竞价核心链路仍可用；客户端按 full jitter 重连并在期限内通过快照恢复，无身份泄漏 |
| `auction-*` | Gateway 明确返回核心命令不可用且不回退到 MySQL 运行态写；恢复后 Redis 快照与版本连续，期间没有错误成交 |
| `enrichment-*` | Projector 与 Domain Relay 持续推进，核心订单保持可查且补全状态明确；恢复后 Kafka lag 归零，重复消息不产生第二条补全记录，同消息异载荷仍拒绝 |
| `index-es-*` | pgvector 与竞价链路不受影响；ES 消费位点不越过失败消息，恢复后按 external version/hash 追平且无旧文档覆盖 |
| `index-pgvector-*` | ES 与竞价链路不受影响；向量消费者恢复后按内容哈希追平，未变化文档不重复向量化且失败消息不被跳过 |
| `close-*` | worker 不可用时进行中出价和反狙击延时继续；恢复后只以 Redis 当前截止时间经 Lua 落槌，零提前成交、零重复终态、到期 ZSET 最终收敛 |
| `nats-*` | Redis Lua 与管理/支付权威写在 NATS 不可用时仍按真实提交结果返回；实时推送停止可见，终态通过心跳/快照恢复；domain outbox 发布失败不会被标记成功，恢复后积压归零且无重复副作用 |
| `elasticsearch-*` | ES 查询失败时受控降级且竞价不受影响；`index-es` 保持 Kafka 位点/重试语义，恢复后追平；展示价格继续来自 Redis/MySQL 权威快照，不能直接采用 ES 文档价格 |

`auction-*`、`relay-*`、`projector-*`、`domain-*`、`enrichment-*`、`index-es-*`、`index-pgvector-*`、`close-*` 与 `gateway-*` 覆盖蓝图中的九个独立应用部署单元。Compose 的 `nats-*` 是单节点 NATS 服务中断，`elasticsearch-*` 是单节点 ES 服务中断；`index-es-*` 中断的是消费进程而不是 ES 服务。它们用于验证应用降级和恢复语义，不得作为蓝图中“仅 Gateway↔NATS 网络分区”或“多节点 ES 当前 master 被 kill 后 30 秒选主”的替代证据；这两项仍必须在具备 NetworkPolicy/故障代理和多节点 ES 的目标环境单独执行。

## 证据

默认写入 `test-results/fault-injection/<run-id>-<scenario>/`：

- `assertion-contract.txt`：断言程序绝对路径、SHA-256 和超时参数。
- `assertion-before.log`、`assertion-during.log`、`assertion-after.log`：三个阶段的原始输出。
- `compose-before.txt`、`compose-after.txt` 与容器 inspect：基础设施状态。
- `result.txt`：只有三个业务断言阶段和服务恢复全部成功时才写 `status=PASS`。

业务断言程序应把 SQL、Kafka offset、Redis Outbox、Prometheus 快照和负载报告继续写入传入的 `result_dir`，以便 CI 作为不可变 artifact 归档。

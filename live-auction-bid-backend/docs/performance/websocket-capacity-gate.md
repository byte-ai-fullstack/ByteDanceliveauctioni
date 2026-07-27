# WebSocket 容量门禁

`TestHubWebSocketLoad` 使用真实 TCP/WebSocket 连接和生产 `realtime.Hub`，但用内存快照替代 Redis、MySQL、Kafka 与 NATS。它用于在基础设施不可用时独立验证单个 Gateway 的连接持有与共享公共快照扇出，不替代 `scripts/load-bid-hot-path.mjs` 的完整链路压测。

## 20,000 连接基线

同一进程同时持有客户端和服务端 socket，因此至少需要约 `2 × connections` 个文件描述符：

```bash
ulimit -n 65535
GO=/home/ye/OpenClaw/state/tools/go/bin/go make ws-load
```

默认参数：

- 20,000 个公共 WebSocket 连接；
- 256 个并行握手 worker；
- 45 秒单连接读取超时；
- 完整终态快照送达 p99 不超过 500ms；
- 输出连接速率、Hub 入队耗时、p50/p95/p99/max、堆增量和每连接堆字节。

Gateway 故障恢复容量门禁在同一批真实连接上追加完整恢复阶段：

```bash
ulimit -n 65535
AUCTION_WS_LOAD_CONNECTIONS=20000 \
AUCTION_WS_LOAD_RECONNECT=1 \
AUCTION_WS_LOAD_RECONNECT_SPREAD=30s \
AUCTION_WS_LOAD_RECOVERY_LIMIT=90s \
GO=/home/ye/OpenClaw/state/tools/go/bin/go make ws-reconnect-load
```

它强制关闭旧 Hub 全部连接，等待旧实例连接数归零，再把连接按确定性的 full-jitter 分散到 30 秒窗口并接入替代 Hub。每个连接必须先收到替代实例的最新完整快照，全部连接必须在 90 秒内恢复；随后再次发布更高版本终态，送达 p99 仍不得超过 500ms，最后新 Hub 连接数也必须归零。该本地门禁验证 Gateway 进程故障下的连接/快照容量语义；Kubernetes 场景仍需真实 kill Pod 并通过 LB 复验。

可通过环境变量覆盖：

```bash
AUCTION_WS_LOAD_CONNECTIONS=2000 \
AUCTION_WS_LOAD_DIAL_WORKERS=128 \
AUCTION_WS_LOAD_READ_TIMEOUT=30s \
AUCTION_WS_LOAD_P99_LIMIT=500ms \
GO=/home/ye/OpenClaw/state/tools/go/bin/go make ws-load
```

## 完整竞价链路

基础设施可用后运行端到端门禁；它会创建拍品和买家、并发出价、等待 Kafka→MySQL 投影追平，并检查所有 WebSocket 的事件覆盖率：

```bash
BASE_URL=http://127.0.0.1:18080 \
GATEWAY_METRICS_URL=http://127.0.0.1:18080/metrics \
PROJECTOR_METRICS_URL=http://127.0.0.1:18083/metrics \
RELAY_METRICS_URL=http://127.0.0.1:18082/metrics \
CONCURRENCY=100 \
WS_CONNECTIONS=20000 \
BID_P99_LIMIT_MS=200 \
MIN_BID_THROUGHPUT_PER_SECOND=100 \
MAX_SYSTEM_ERROR_RATE=0 \
REPORT_FILE=test-results/ws-20000.json \
node scripts/load-bid-hot-path.mjs
```

三个 metrics URL 必须指向各自部署单元。脚本会拒绝在拆分部署中复用 Gateway URL，并在 Projector/Relay 缺少必需指标族时失败，防止将“该进程没有注册此指标”误判为“lag/pending 为 0”。

最终验收必须同时保留两份证据：独立 Gateway 容量结果，以及包含 Redis Lua、Redis Outbox、Kafka、Projector 和 MySQL 的端到端结果。

`load-bid-hot-path.mjs` 是单热门拍品的一次性并发突发门禁，测量窗口只覆盖并发出价批次，不把账号/拍品准备时间计入吞吐。默认硬失败条件为：系统错误率 `0`、出价响应 p99 严格 `<200ms`、批次吞吐 `≥100 req/s`。业务拒绝不算系统错误，但最终价格、领先者、幂等、Outbox ACK、Kafka→MySQL 追平和 WS 覆盖仍必须全部成立。该结果证明单 key 突发能力，不替代全站 `200 req/s` 的开放到达率稳态测试。

单热门拍品的开放到达率场景复用买家池，并在响应变慢时仍按计划发出后续请求：

```bash
CONCURRENCY=100 \
TARGET_BID_RATE_PER_SECOND=100 \
LOAD_DURATION_SECONDS=60 \
BID_P99_LIMIT_MS=200 \
MIN_BID_THROUGHPUT_PER_SECOND=95 \
MIN_OFFERED_RATE_RATIO=0.99 \
MAX_SCHEDULE_DRIFT_P99_MS=100 \
REPORT_FILE=test-results/hot-lot-open-rate-100.json \
node scripts/load-bid-hot-path.mjs
```

该模式计划 `rate × duration` 次请求，报告实际请求起始速率和发压调度漂移；实际到达率低于目标的 99% 或调度漂移 p99 超过 `100ms` 时判定发压机证据无效。完成吞吐默认允许 5% 尾部收敛余量，但响应 p99、系统错误、Redis Outbox ACK 和 Projector 追平仍使用同一硬门禁。

全站峰值 `≥200 req/s` 使用两个独立商家、房间和热门拍品，每条链路以 `105 req/s` 开放到达率运行。总控会等待两个子场景都完成数据与指标基线准备，再用共同的未来时间同时放行：

```bash
CONFIRM_BID_LOAD=1 \
SCENARIOS=2 \
RATE_PER_SCENARIO=105 \
LOAD_DURATION_SECONDS=60 \
BIDDERS_PER_SCENARIO=100 \
MIN_AGGREGATE_OFFERED_RATE=200 \
MIN_OVERLAP_RATIO=0.95 \
BID_P99_LIMIT_MS=200 \
MAX_SYSTEM_ERROR_RATE=0 \
REPORT_FILE=test-results/bid-fleet/fleet-200.json \
node scripts/load-bid-fleet.mjs
```

最终报告拒绝把先后执行的子结果相加：两个测量窗口至少重叠 95%，实际到达率之和至少 `200 req/s`，任一子场景失败、聚合系统错误率超限或任一子场景 p99 达到 `200ms` 都返回非零。每个子场景仍独立验证价格/领先者、Outbox ACK、Kafka→MySQL 追平和投影不变量。

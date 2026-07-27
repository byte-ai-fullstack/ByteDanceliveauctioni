# 单 Gateway 20,000 WebSocket 容量结果（2026-07-26）

## 结论

独立 Gateway 容量门禁与本地进程替换恢复门禁均通过。基线中 20,000 条真实 loopback WebSocket 连接全部建立并收到同一份完整终态公共快照，终态送达 p99 为 `109.84ms`；2026-07-27 的恢复场景中，同规模连接被旧 Hub 全部断开并按 30 秒 full-jitter 接入替代 Hub，最慢 `29.998s` 恢复，恢复后的终态送达 p99 为 `159.30ms`。两次广播均低于蓝图 `500ms` 目标，恢复低于 `90s` 目标。

这份结果验证 Gateway 连接持有、房间级共享编码、有界 `latestState` 队列和写协程扇出；它不包含 Redis Lua、Redis Outbox、Kafka、Projector、MySQL 或 NATS 网络开销，不能替代完整竞价链路报告。

## 环境

- WSL2 Ubuntu 24.04；
- 32 个逻辑 CPU；
- 约 11GiB 可见内存；
- 测试进程 `ulimit -n 65535`；
- Go：`/home/ye/OpenClaw/state/tools/go/bin/go`；
- 客户端与 Gateway 在同一测试进程，使用真实 TCP/WebSocket。

## 命令

```bash
ulimit -n 65535
AUCTION_WS_LOAD_CONNECTIONS=20000 \
AUCTION_WS_LOAD_DIAL_WORKERS=256 \
/home/ye/OpenClaw/state/tools/go/bin/go test \
  -run '^TestHubWebSocketLoad$' \
  -count=1 -timeout=10m -v \
  ./app/auction/service/internal/realtime
```

## 结果

| 指标 | 结果 |
|---|---:|
| 请求连接数 | 20,000 |
| 成功连接数 | 20,000 |
| 连接建立耗时 | 862.26ms |
| 连接建立速率 | 23,194.8/s |
| 终态入队耗时 | 95.20ms |
| 终态送达 p50 | 99.39ms |
| 终态送达 p95 | 109.08ms |
| 终态送达 p99 | 109.84ms |
| 终态送达最大值 | 110.27ms |
| Go 堆增量 | 424,382,616 bytes |
| 每连接 Go 堆增量 | 21,219 bytes |
| 门禁结果 | PASS |

## 进程替换恢复复验（2026-07-27）

```bash
ulimit -n 65535
AUCTION_WS_LOAD_CONNECTIONS=20000 \
AUCTION_WS_LOAD_DIAL_WORKERS=256 \
AUCTION_WS_LOAD_READ_TIMEOUT=45s \
AUCTION_WS_LOAD_P99_LIMIT=500ms \
AUCTION_WS_LOAD_RECONNECT=1 \
AUCTION_WS_LOAD_RECONNECT_SPREAD=30s \
AUCTION_WS_LOAD_RECOVERY_LIMIT=90s \
/home/ye/OpenClaw/state/tools/go/bin/go test \
  -run '^TestHubWebSocketLoad$' \
  -count=1 -timeout=10m -v \
  ./app/auction/service/internal/realtime
```

| 指标 | 结果 |
|---|---:|
| 请求/成功连接数 | 20,000 / 20,000 |
| 初次连接建立耗时 | 879.32ms |
| 初次连接建立速率 | 22,744.8/s |
| 首轮快照入队耗时 | 107.60ms |
| 首轮快照送达 p99 | 117.74ms |
| 重连 full-jitter 窗口 | 30s |
| 重连恢复 p50 / p95 / p99 | 14.954s / 28.450s / 29.690s |
| 全部连接恢复最大耗时 | 29.998s |
| 恢复后终态入队耗时 | 17.19ms |
| 恢复后终态送达 p99 | 159.30ms |
| 旧 Hub / 替代 Hub 最终连接数 | 0 / 0 |
| 门禁结果 | PASS |

该场景使用两个真实 `httptest.Server` 和生产 `realtime.Hub` 模拟旧 Gateway 进程退出与替代进程接管：连接必须从替代 Hub 获得版本 2 的权威快照，再收到版本 3 的增量事件。它验证了单机进程替换语义，但不声称覆盖 Kubernetes Service/LB 路由、termination grace period 或跨 Pod 网络，因此目标集群仍需真实 `kill Pod` 复验。

## 尚需保留的验收项

基础设施可用后，仍需运行 `scripts/load-bid-hot-path.mjs` 的 `WS_CONNECTIONS=20000` 场景，保存包含 Redis Lua、Redis Outbox、Kafka 投影追平、MySQL 和 NATS 的端到端报告；Kubernetes 发布期还需通过真实 Service/LB 验证 Gateway Pod 退出时的 20,000 连接分批重连风暴。

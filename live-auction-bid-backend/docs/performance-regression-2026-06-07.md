# LiveAuction Suite 周期报告｜性能优化部署与回归测试

报告版本：2026-06-07  
文档类型：性能优化回归报告  
内容范围：后端性能改造、服务器部署、公网自动化压测、运行指标复核  
测试环境：`http://120.79.7.110`

> 本次报告记录 2026-06-07 后端性能优化上线后的验证结果。测试重点是出价热路径、运行态投影批处理、Redis/MySQL 连接池、Nginx 访问日志和公网链路稳定性。

## 分卷导航

| 分卷 | 内容 | 本次状态 |
| --- | --- | --- |
| 1 | 代码改造与上线 | 已完成 |
| 2 | 自动化验证 | 已完成 |
| 3 | 公网压测结果 | 已完成 |
| 4 | 服务器指标复核 | 已完成 |
| 5 | 风险与后续动作 | 已记录 |

## 8. 测试与性能验证

### 8.1 自动化验证

| 验证项 | 命令 / 方式 | 结果 |
| --- | --- | --- |
| 后端单元测试 | `go test ./...` | 通过 |
| 部署脚本语法检查 | `bash -n scripts/deploy-prod.sh` | 通过 |
| Nginx 配置检查 | `nginx -t` | 通过 |
| 后端健康检查 | `curl http://127.0.0.1/readyz` | `{"ok":true}` |
| H5 入口检查 | `curl http://127.0.0.1/` | HTTP 200 |
| Admin 入口检查 | `curl http://127.0.0.1:8080/` | HTTP 200 |

### 8.2 压测环境与测试目标

| 项 | 内容 |
| --- | --- |
| 服务器 | 2 vCPU / 3.4GiB 内存 / 49G 系统盘 |
| 系统 | Ubuntu Linux 6.8 |
| 后端镜像 | `live-auction-bid-backend:prod` |
| 后端提交 | `8083657 perf(auction): add projection backpressure and batching` |
| 测试时间 | 2026-06-07 15:56:23 - 16:02:24 |
| 压测入口 | 公网 `http://120.79.7.110` |
| 核心目标 | 验证出价热路径不被 MySQL 投影拖慢，投影队列无积压，公网链路无 5xx 和网络错误 |

### 8.3 测试套件流程

本次沿用项目已有自动化压测工具，测试顺序为：

1. 冒烟验证：少量用户慢速出价，确认部署后基础链路可用。
2. 可视化低速：模拟直播间正常低频出价。
3. 可视化中速：提高出价密度，观察投影是否稳定。
4. 可视化快速：持续快速出价，验证 Web/H5 可见链路。
5. 轻量并发：50 并发瞬时竞争同一拍品。
6. 中等并发：100 并发、两轮竞争同一拍品。

原始测试命令：

```bash
go run . test-suite \
  --base-url http://120.79.7.110 \
  --reports-dir reports/suite-20260607-155623 \
  --output reports/latest-suite-summary-deploy-20260607-155623.md
```

### 8.4 最新公网压测结果总览

| 用例 | 请求数 | Accepted | Rejected | Internal Errors | HTTP 5xx | 网络错误 | Projection | Avg | P95 | P99 | Throughput | 结果 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | ---: | ---: | --- |
| smoke-live | 10 | 10 | 0 | 0 | 0 | 0 | true / 1067ms | 12ms | 15ms | 15ms | 0.36/s | PASS |
| visual-slow | 60 | 60 | 0 | 0 | 0 | 0 | true / 545ms | 12ms | 15ms | 16ms | 0.48/s | PASS |
| visual-medium | 180 | 180 | 0 | 0 | 0 | 0 | true / 2116ms | 12ms | 14ms | 15ms | 1.88/s | PASS |
| visual-fast | 300 | 300 | 0 | 0 | 0 | 0 | true / 1050ms | 11ms | 14ms | 17ms | 4.31/s | PASS |
| concurrent-light | 100 | 19 | 81 | 0 | 0 | 0 | true / 556ms | 36ms | 69ms | 85ms | 148.03/s | PASS |
| concurrent-medium | 400 | 61 | 339 | 0 | 0 | 0 | true / 1588ms | 46ms | 110ms | 164ms | 219.70/s | PASS |

说明：并发场景中的 `Rejected` 为业务规则拒绝，主要原因是同一拍品高并发下部分请求低于最新价，后端返回 `BID_TOO_LOW`。该结果不属于系统错误。

### 8.5 出价结果组成图

图表源文件：

```text
tmp/auction-stress-test-main/../项目汇报材料/压测报告/2026-06-07-155623/charts/bid-outcomes.svg
```

核心观察：

| 场景 | 观察 |
| --- | --- |
| 直播节奏场景 | Accepted 占比 100%，说明常规直播出价链路稳定 |
| 并发竞争场景 | Rejected 占比较高但均为业务拒绝，说明价格竞争保护生效 |
| 错误项 | Internal Errors、HTTP 5xx、网络错误均为 0 |

### 8.6 延迟百分位图

图表源文件：

```text
tmp/auction-stress-test-main/../项目汇报材料/压测报告/2026-06-07-155623/charts/latency-percentiles.svg
```

延迟结论：

| 场景 | P50 | P95 | P99 | 判断 |
| --- | ---: | ---: | ---: | --- |
| smoke-live | 12ms | 15ms | 15ms | 正常 |
| visual-fast | 11ms | 14ms | 17ms | 正常 |
| concurrent-light | 29ms | 69ms | 85ms | 正常 |
| concurrent-medium | 35ms | 110ms | 164ms | 正常 |

本次优化后，出价请求先在 Redis 原子脚本中完成价格校验、版本推进和事件写入，再由后台投影批量落 MySQL。公网中等并发下 P99 为 164ms，未出现秒级长尾。

### 8.7 延迟分布曲线

图表源文件：

```text
tmp/auction-stress-test-main/../项目汇报材料/压测报告/2026-06-07-155623/charts/latency-curves.svg
```

延迟分布表现：

| 指标 | 结果 |
| --- | --- |
| Accepted 平均延迟 | 约 4.18ms，Prometheus 统计 `2762ms / 660` |
| Rejected 平均延迟 | 约 18.32ms，Prometheus 统计 `7693ms / 420` |
| MySQL 连接池等待 | 0 |
| Redis 连接池 timeout | 0 |

### 8.8 吞吐图

图表源文件：

```text
tmp/auction-stress-test-main/../项目汇报材料/压测报告/2026-06-07-155623/charts/throughput.svg
```

吞吐观察：

| 场景 | Throughput | 说明 |
| --- | ---: | --- |
| visual-fast | 4.31/s | 持续出价节奏，全部接受 |
| concurrent-light | 148.03/s | 50 并发瞬时竞争，无 5xx |
| concurrent-medium | 219.70/s | 100 并发两轮竞争，无 5xx |

### 8.9 关键压测结论

| 结论 | 证据 |
| --- | --- |
| 部署后服务可用 | 后端 healthy，H5/Admin 均 HTTP 200 |
| 出价热路径稳定 | 6 个测试场景全部 PASS |
| 公网链路没有系统级错误 | HTTP 5xx=0，网络错误=0，Internal Errors=0 |
| 投影批处理没有产生积压 | `auction_projection_pending_count=0`，`auction_projection_lag_ms=0` |
| 投影没有版本断层 | `auction_projection_gap_total=0` |
| 投影没有处理失败 | `auction_projection_failed_total=0` |
| MySQL 未出现连接池争抢 | `auction_db_pool_wait_count_total=0` |
| Redis 未出现连接池超时 | `auction_redis_pool_timeouts_total=0` |

### 8.10 本次优化与修复效果

| 优化项 | 原来 | 改后 | 验证结果 |
| --- | --- | --- | --- |
| 运行态投影 | 单条事件逐条解码、逐条写库 | 按 shard 批量读取、批量解码、单事务批量投影 | 公网压测后 pending=0 |
| 投影 ACK | 每条消息独立 ACK | 批量 ACK | 无投影积压，无 gap |
| 投影背压 | 出价热路径只管写 stream | Redis Lua 中读取 pending/lag，超过阈值时可拒绝新写入 | 本次未触发背压，链路保持稳定 |
| Nginx 日志 | 普通访问日志，可观测性弱 | JSON access log，记录 request/upstream time 和 request_id | 已生成 bid/admin/h5 分路日志 |
| Nginx worker | 默认连接配置偏保守 | `worker_connections 8192`，`worker_rlimit_nofile 65535` | Nginx reload 成功 |

### 8.11 本次部署记录

| 时间 | 动作 | 结果 |
| --- | --- | --- |
| 2026-06-07 15:54 | 本地构建后端镜像 | 成功 |
| 2026-06-07 15:55 | 上传镜像与源码到服务器 | 成功 |
| 2026-06-07 15:55 | 重建后端容器 | 成功，容器 healthy |
| 2026-06-07 15:55 | Nginx 配置检查 | 首次失败，原因是旧站点 symlink 与新站点配置重复定义 `auction_backend` upstream |
| 2026-06-07 15:55 | 删除旧 symlink `/etc/nginx/sites-enabled/live-auction.conf` | 成功 |
| 2026-06-07 15:55 | 重新执行 `nginx -t` 与 reload | 成功 |

## 9. 风险与后续动作

| 项 | 当前判断 | 后续动作 |
| --- | --- | --- |
| 并发业务拒绝 | 属于正常价格竞争拒绝，不是服务错误 | H5 可优化本人失败提示范围，避免公共直播间被失败提示污染 |
| 高频弹幕闪动 | 属于前端展示体验问题 | H5 对出价弹幕做合并、节流或排行榜优先展示 |
| 2C/3.4GiB 服务器容量 | 当前压测可支撑 100 并发两轮竞价，公网吞吐 219.70/s 无 5xx | 更高并发前需要单独做阶梯压测，不直接承诺 3000 QPS |
| 投影背压阈值 | 当前未触发，说明投影可消化本次压力 | 后续压更高档时观察背压触发次数和用户侧错误文案 |

# 附录 A：压测数据明细

## A.1 公网真实链路结果

| 用例 | 命令等价参数 | H5 直播间 | 原始 JSON |
| --- | --- | --- | --- |
| smoke-live | `users=3 interval=3s duration=30s` | `http://120.79.7.110/m/room/321920395596791808` | `reports/suite-20260607-155623/smoke-live/report-20260607-155717.json` |
| visual-slow | `users=30 interval=2s duration=2m0s` | `http://120.79.7.110/m/room/321920515096707072` | `reports/suite-20260607-155623/visual-slow/report-20260607-155913.json` |
| visual-medium | `users=50 interval=500ms duration=1m30s` | `http://120.79.7.110/m/room/321921010880217088` | `reports/suite-20260607-155623/visual-medium/report-20260607-160037.json` |
| visual-fast | `users=50 interval=200ms duration=1m0s` | `http://120.79.7.110/m/room/321921396613578752` | `reports/suite-20260607-155623/visual-fast/report-20260607-160154.json` |
| concurrent-light | `users=100 concurrency=50 rounds=1` | `http://120.79.7.110/m/room/321921700943888384` | `reports/suite-20260607-155623/concurrent-light/report-20260607-160155.json` |
| concurrent-medium | `users=200 concurrency=100 rounds=2` | `http://120.79.7.110/m/room/321921782271442944` | `reports/suite-20260607-155623/concurrent-medium/report-20260607-160224.json` |

## A.2 服务器运行指标

| 指标 | 值 |
| --- | ---: |
| `auction_bid_accepted_total` | 660 |
| `auction_bid_rejected_total{reason="BID_TOO_LOW"}` | 420 |
| accepted runtime facts (legacy baseline counter) | 12035 |
| `auction_projection_pending_count` | 0 |
| `auction_projection_lag_ms` | 0 |
| `auction_projection_failed_total` | 0 |
| `auction_projection_gap_total` | 0 |
| `auction_db_pool_wait_count_total` | 0 |
| `auction_db_pool_open_connections` | 3 |
| `auction_redis_pool_total_conns` | 105 |
| `auction_redis_pool_timeouts_total` | 0 |

## A.3 Nginx 复核结果

| 文件 | 状态 |
| --- | --- |
| `/var/log/nginx/live-auction-bid.access.log` | 已生成，约 334K |
| `/var/log/nginx/live-auction-h5.access.log` | 已生成，约 340K |
| `/var/log/nginx/live-auction-admin.access.log` | 已生成，约 12K |

压测尾部 bid 日志样例显示请求均返回 HTTP 200，`request_time` 约 0.088s - 0.094s，`upstream_response_time` 约 0.088s - 0.094s。

## A.4 原始报告位置

| 类型 | 路径 |
| --- | --- |
| 自动化套件汇总 | `tmp/auction-stress-test-main/reports/latest-suite-summary-deploy-20260607-155623.md` |
| 图表与原始数据包 | `tmp/auction-stress-test-main/../项目汇报材料/压测报告/2026-06-07-155623/` |
| 套件分场景报告 | `tmp/auction-stress-test-main/reports/suite-20260607-155623/` |

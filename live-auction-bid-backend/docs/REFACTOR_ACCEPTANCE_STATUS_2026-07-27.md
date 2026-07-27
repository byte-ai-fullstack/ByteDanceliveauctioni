# 重构验收状态（2026-07-27）

本文记录 `docs/REFACTORING_PLAN.md` 的当前可复验证据。状态只根据代码、自动化测试和已实际执行的命令填写；容量、跨可用区和业务风险接受不能由单元测试替代。

## 分层状态

| 层级 | 状态 | 已有证据 | 尚未关闭 |
|---|---|---|---|
| L0 工程门禁 | 自动化通过 | 后端 CI 包含 lint、vet、race、integration、镜像构建和核心覆盖率；golangci depguard 强制领域包不得导入 GORM/Redis/Kafka/NATS/HTTP 及内部基础设施适配器；单热拍品突发/开放到达率与双热拍品全站吞吐脚本均以错误率、p99、实际到达率、调度漂移、窗口重叠和投影追平作为硬退出条件；PC/H5 各自有测试、lint、类型检查和构建门禁，并通过共享 UI 债务守卫；H5 表现冻结清单、PC 视觉锁和外部资产检查均通过 | 本次未取得远端 CI run URL |
| L1 契约与数据模型 | 自动化通过 | Buf 生成门禁、Runtime/Domain/Realtime proto、空库迁移与回滚、MySQL/PostgreSQL schema 测试 | 生产备份恢复仍需周期演练 |
| L2 基础设施 | 本地自动化通过 | Kafka/Redis/NATS/ES/pgvector Compose、3 节点 HA 演练脚本、Kubernetes 源/发布共 151 个资源严格校验、Kubernetes 发布清单强制版本+digest、生产 Compose 强制已校验的后端/ES image ID、固定基础镜像与 exporter 告警；宿主机磁盘、PVC、宿主机内存、Redis maxmemory 和 ES heap 均有 75/85% 规则与 promtool 触发用例，固定 Prometheus 容器完成配置及规则校验；统一 Compose 故障门禁覆盖 Redis/MySQL/Kafka/NATS/ES 与所有核心部署单元，强制 before/during/after 业务断言；旧静态分片和应用注册中心已删除，gateway 与无状态 auction-service 只经平台 DNS 发现；隔离 project `live-auction-l2-smoke-20260727f` 已通过六个核心基础设施和 7 个 exporter 的运行语义断言；隔离 project `live-auction-l2-ha-20260727b` 已通过 Kafka 3 broker、Redis 1 主 2 从 + 3 Sentinel、NATS 3 节点故障演练 | NATS 定向网络分区、ES 多节点选主、托管服务指标映射及跨机/AZ 演练仍需目标环境证据 |
| L3 Runtime 与事件主干 | 自动化通过 | 5 个 Lua 命令、Redis List Outbox、fencing Relay、Kafka Projector、DB offset/inbox/domain outbox、generation freeze、属性与并发测试；真实 Redis 门禁同时覆盖截止边界的一次出价/落槌线性化，以及截止前 100ms 尾拍延时后持续出价、close-worker 每 2ms 判定并跨越原截止时间的场景，要求零提前成交、版本/outbox 连续且到期 ZSET 与最终 Redis 截止时间一致；auction-service 直接读取 Kafka 边界/最老未投影记录与 MySQL DB offset，按积压、时龄、保留余量和结构异常 fail closed，开拍/出价/直播配置同步在任何存储 I/O 前返回 `OVERLOADED`，取消与到期落槌不受影响；Projector 分配时强制按 MySQL offset seek；原始 Runtime Topic gap 支持受控重放，retention cliff 支持只读、双人审批、逐 offset 完整事实的 synthetic repair，同 bundle 前缀续跑和 completion-only 均核验 inbox、DB offset 及 `last_event_id/version/canonical_hash`；Kafka 初始化器对所有既有 Topic 分区数严格拒绝漂移，不再静默在线扩容；Outbox 拓扑固定为 16，生产命令启动拒绝漂移且所有受控部署清单禁止环境插值覆盖 | Redis 主从同时故障残余窗口必须由业务签字；新增真实 Redis 竞态和 synthetic 审计 MySQL 用例仍需远端 CI 证据；真实 Relay/Projector/repair kill、投影闸门关闭/恢复、网络隔离及分区/分片迁移演练需目标环境复验 |
| L4a WebSocket/NATS | 本地容量门禁通过 | 公共/私有/运营消息、隐私字节测试、慢消费者、心跳回源、订单 READY 私有推送与轮询恢复、NATS 丢终态恢复、订阅数与非空房间数漂移告警、PC/H5 代际隔离与重连测试；20,000 条真实 TCP/WS 连接已完成旧 Hub 强制断开、30 秒 full-jitter 接入替代 Hub、最新快照恢复、后续终态送达与双端零连接泄漏，最大恢复 `29.998s`，恢复后广播 p99 `159.30ms` | 真实 Kubernetes kill Pod + Service/LB 路由、termination grace period 仍需目标环境证据 |
| L4b Domain/Enrichment | 自动化门禁已接入 | Domain Relay claim token/fencing、每 `topic+partition_key` 头阻塞、批内串行与首错阻断及真实 MySQL 集成用例；独立 enrichment worker 只消费订单补全 Topic，真实 MySQL 用例覆盖核心订单/拍品关联、地址和店铺快照、可选来源缺失时落 `PARTIAL` 而非阻塞、重复消息幂等及同消息异载荷拒绝 | 新增 Enrichment MySQL 用例本地因无测试 DSN 仅完成编译，需取得远端 CI run；目标环境仍需执行 domain-relay/enrichment-consumer pause/kill、Kafka commit/DLQ 和长时间来源不可用演练 |
| L5 检索 | 功能门禁通过 | ES/pgvector 独立消费者、external version/hash、重建/alias、对账修复、降级和成本指标；检索 SLO 已固定为 ES/pgvector 各自 p99 `< 1000ms`，具备 Prometheus 告警、Grafana 面板和按本轮直方图增量判定的目标环境压测门禁 | 生产规格下执行 5000 次双索引检索并完成持续写重建压测 |
| L6 零残留与总验收 | 部分通过 | `check-zero-residue.sh` 与 L3 wiring 检查通过；旧事件链、静态 shard gateway、应用内注册、手写 autoscale、旧监控/部署配置及其当前文档零残留；`deploy/.env` 已取消 Git 跟踪并保持本地忽略，部署归档与镜像排除该文件，CI 对完整 tracked tree 执行固定版本 Gitleaks 扫描 | Git 历史 secret scan/凭据轮换、全量混沌和容量验收未完成 |

## 本次本地复验

以下命令已在 2026-07-27 执行成功：

```text
go vet ./...
go build ./...
golangci-lint run --timeout=8m
go test -race ./... -count=1
go test -race ./app/auction/service/internal/... -count=1
go test -race ./app/auction/service/internal/worker/domainrelay -count=1
go test -tags=integration ./... -count=1
go run github.com/bufbuild/buf/cmd/buf@v1.50.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.50.0 build
go run github.com/bufbuild/buf/cmd/buf@v1.50.0 generate
git diff --exit-code -- api
bash scripts/check-core-coverage.sh
bash scripts/check-kubernetes-manifests.sh
bash scripts/check-l2-monitoring.sh
bash scripts/check-l3-runtime-wiring.sh
bash scripts/check-zero-residue.sh
bash test/infra/deploy-secret-boundary.test.sh
bash test/infra/prod-release-images.test.sh
promtool check rules deploy/prometheus/rules/target-infrastructure.yml
promtool test rules test/infra/prometheus-rules.test.yml
node --test scripts/load-search-retrieval.test.mjs
node --test scripts/load-bid-hot-path.test.mjs
node --test scripts/load-bid-fleet.test.mjs
bash test/infra/kafka-init-topics.test.sh
bash test/infra/fault-injection.test.sh
node --test scripts/assert-projector-recovery.test.mjs scripts/assert-relay-recovery.test.mjs
bash ../scripts/check-frontend-quality.sh
```

核心覆盖率门禁结果：

| 包 | 覆盖率 |
|---|---:|
| `biz/auction` | 83.50% |
| `eventcontract` | 83.84% |
| `gateway` | 94.12% |
| `kafkaclient` | 91.84% |
| `projectiongate` | 86.58% |
| `projectionrepair` | 80.60% |
| `realtime` | 80.41% |
| `runtimegeneration` | 88.24% |
| `worker/closeworker` | 94.31% |
| `worker/domainrelay` | 82.70% |
| `worker/outboxrelay` | 83.27% |
| `worker/projector` | 81.27% |

前端本地复验：

| 应用 | 测试 | 其他门禁 |
|---|---:|---|
| PC 商家端 | 53/53 | TypeScript、ESLint、Knip、Vite build、视觉锁、共享 UI 债务守卫通过 |
| H5 买家端 | 43/43 | TypeScript、ESLint、契约守卫、表现冻结、外部资产、Vite build、共享 UI 债务守卫通过 |

L2 Prometheus 配置和规则已通过仓库固定的 Prometheus 容器校验。完整 Compose smoke 使用隔离 project `live-auction-l2-smoke-20260727f` 真实执行并输出 `status=passed topics=7 mysql_durability=$'1\t1\tROW'`：Kafka、Redis、MySQL、NATS、Elasticsearch 8.19.17 + analysis-ik、pgvector 与 7 个 exporter 全部健康，Topic 数量/保留期、Redis AOF/maxmemory、MySQL durable binlog、NATS/ES/pgvector 及 exporter 指标均通过断言。为兼容 Docker Desktop，IK 插件改为固定 SHA-256 的远程构建输入，本地 node-exporter 禁用无法解析 WSL `9p` mountinfo 的 filesystem collector；生产 Compose 继续在原生 Linux 上保留 `rslave` 和 filesystem collector。3 节点 HA drill 使用隔离 project `live-auction-l2-ha-20260727b` 输出 `status=passed`：Kafka 验证单 broker 故障可用、双 broker 故障拒绝和已确认记录保留；Redis 验证 Sentinel 换主、已确认 key 保留、run_id 代际变化及旧主恢复为副本；NATS 验证单节点故障后的路由可用与恢复。Kubernetes 渲染、严格 schema、镜像锁和连接预算等静态门禁已通过。

## P0 风险证据

| 风险 | 状态 | 证据/结论 |
|---|---|---|
| R1 生命周期绕过 Lua | 自动化保护 | L3 wiring 静态检查；开拍、出价、同步、取消、落槌统一走 Runtime command store；真实 Redis 并发测试既证明截止瞬间只能由尾拍或落槌之一提交，也证明尾拍将截止时间原子延长后，120 次高频落槌 tick 跨越原截止时间仍全部返回 `NOT_EXPIRED`，最终状态、版本、outbox 和到期 ZSET 保持一致 |
| R2 Lua 写段运行时错误 | 自动化保护 | READ/VALIDATE/WRITE 静态规约；错误 Key 类型写前预检；7 个真实 Redis 零写入回归场景 |
| R3 Redis 主从同时丢失 | 待业务接受 | `WAIT`、min replicas、AOF、generation freeze 和双向对账只能缩小窗口，不能证明零丢失 |
| R4 EVAL/WAIT 不同连接 | 实现已保护 | command store 使用同一个专用 `redis.Conn` 顺序执行脚本与 `WAIT`；仍应在 HA 环境复验确认语义 |
| R5 Failover 通知丢失 | 自动化保护 | Sentinel signal + run_id 轮询；generation 变化冻结、核对后解冻测试 |
| R6 旧 Relay 删除新 inflight | 自动化保护 | acquire/take/ack/renew/release 双端 fencing 与接管 FIFO 测试 |
| R7 Relay 接管乱序 | 本地自动化关闭，目标演练待复验 | fenced take/peek/ACK/renew/release 模型测试通过；仓库自带三阶段 Relay pause/kill 断言器，故障期直接核对 16 个 Redis pending/inflight 队列，恢复期要求队列清零、owner 完整、kill 后 epoch 推进、ACK 异常为零、成功 ACK 覆盖注入事实，并以 MySQL inbox/lot/projection 版本链和幂等键证明去重后零 gap；归档不含凭据 | 真实 Redis/Kafka/MySQL 环境仍需执行持有租约的 Relay kill 并归档证据 |
| R8 DB 与 Kafka 双提交 | 自动化保护 | 业务投影、inbox、domain outbox、DB offset 同事务；失败回滚、重复、gap、deadlock 测试 |
| R10 event_id 内容冲突 | 自动化保护 | payload hash 冲突冻结 lot/partition 并记录 P0 finding |
| R11 Runtime poison/gap 后无法安全恢复 | 本地功能关闭，CI/演练待复验 | Projector 只暂停故障 partition；保留期内按原始事实受控重放；越过 retention 时，`synthetic` 只接受 no-follow、只读、精确 SHA-256 的人工复核 bundle，要求 preparer/executor 分离、每 offset 一条完整 Runtime Fact、全局 event ID/inbox/版本链预检，并只经现有 Projector 事务落库；同 bundle 前缀续跑、completion-only、Kafka earliest 前移拒绝 `resume_safe`、审计和最终三元组核验均有单元测试；新增 MySQL 审计集成用例待远端 CI 实跑，目标环境还需 repair kill 演练 |
| R14 成交成功但订单暂不可见 | 自动化保护 | `/api/rooms/{id}/me` 在投影未可查时返回 `202 + Retry-After + CREATING`；domain-relay 只在 Kafka ACK 和 MySQL outbox 标记成功后通过独立 NATS principal 私推 READY；NATS 失败不改变已提交发布结果，丢信号由该接口轮询恢复；5s/30s visibility lag 告警有 promtool 触发用例 |
| R15 公共 WS 泄漏身份 | 自动化保护 | 最终序列化字节隐私测试；公共、个人、运营 payload 分离 |
| R22 Projector 停滞但 Redis Outbox 已清空 | 本地自动化关闭，CI/目标演练待复验 | auction-service 以无 group、无 commit、只读直分区客户端采样 Runtime Topic earliest/latest 和 DB next offset 的精确记录时间戳，结合共享 MySQL 连接池形成缓存快照；分区/offset/retention cliff/积压/最老时龄/保留余量/依赖和快照过期均立即 fail closed，连续 3 次健康才恢复；`OVERLOADED` 协议、独立 `/admissionz`、服务 `/readyz` 与闸门解耦、liveness 独立性、零 I/O 拒绝、并发 race、86.58% 核心包覆盖率、独立 Kafka Secret/最小 ACL、Compose/Kubernetes、Prometheus 规则和 Grafana 面板均有自动化验证；仓库自带三阶段 pause/kill 断言器，自动造事实、证明拒绝零新增 Kafka offset、关闭期取消可用、恢复三采样及 MySQL 唯一性/版本链/P0-P1/domain outbox 收敛，并保证归档无令牌；真实 MySQL offset 查询用例已接入 CI，本地因没有测试 DSN 未实跑 | 仍需取得远端 CI run，并在目标环境实际执行 Projector pause/kill 后归档断言证据 |
| R24 持久组件容量耗尽 | 部分关闭 | 全部非临时宿主机挂载、Kubernetes PVC、宿主机内存、Redis maxmemory 与 ES heap 均有 75/85% 告警和 promtool 触发测试；托管服务指标映射与真实写满/内存压力演练仍需目标环境证据 |
| R25 凭据进入仓库/镜像 | 当前树自动保护，历史待处置 | `deploy/.env` 已取消 Git 跟踪且被 `.gitignore`、Docker context 和部署归档共同排除；CI 先断言该路径未被跟踪，再对完整 tracked tree 执行固定版本 Gitleaks 扫描，生产要求预置 0600 Secret；历史提交仍需单独授权扫描、轮换及必要时清理 |
| R26 两套链路并存 | 自动化保护 | 旧流式链路零残留和生产 wiring 检查通过，无旧链路开关 |
| R27 两套服务拓扑/发现/扩容控制面并存 | 自动化保护 | 静态实例 JSON、shard gateway、应用内注册、手写 autoscale、旧 Compose/监控、聚合端点及过渡 `monolith` NATS principal 已删除；零残留和 K8s 渲染门禁要求独立 `gateway`/`auction-service` Deployment |
| R28 Domain Relay同路由乱序 | 自动化保护 | SQL 只 claim 每个 `topic+partition_key` 最早未发布行；前序租约/退避期间阻断后继；批内按 outbox id 串行并在首错后停止该路由；单元、race 与 CI MySQL 集成回归覆盖 |
| R29 已提交写被NATS失败改写为失败响应 | 自动化保护 | 创建、排队、展示态和支付在业务存储提交后只做 best-effort Core NATS 扇出；失败记录后由权威快照恢复，不再改变 API 成功结果；失败注入覆盖管理态、展示态和支付 |

## 2026-07-27 IMPL-003 补充复验

- 修正后的 `relay-kill` 真实门禁已通过；恢复路径改为身份保持的 `docker compose start <service>`，只有在无法启动既有容器时才 fallback 到 `up -d --no-deps`，不再重建 Kafka/Redis/MySQL 依赖。证据目录：`test-results/fault-injection/impl003-relay-kill-20260727a-relay-kill`。
- 修正后的 `projector-kill` 真实门禁已通过。第一次重跑暴露的不是业务断言错误，而是脚本把两个 buyer 的地址/押金准备并发执行，真实后端 `user_delivery_addresses` 事务返回 `Error 1213 deadlock`；脚本改为串行准备后复验通过。证据目录：`test-results/fault-injection/impl003-projector-kill-r2-20260727a-projector-kill`。
- 隔离 stack 当前已恢复到关键服务全部 `healthy`，包括此前残留的 `projector`。这说明在不重建依赖的恢复路径下，Projector 可以重新入组并通过 readiness。
- `load-bid-hot-path` 与 `load-bid-fleet` 本地复验未进入容量断言，前置失败点一致：运行中的 `gateway`/worker metrics 没有 `go_build_info` family；当前隔离镜像 `live-auction-bid-backend:impl001-20260727a` 需要在包含兼容补丁的工作树上重建后再复验。
- `load-search-retrieval` 本地复验同样因 `go_build_info` 缺失提前失败；另外当前 `gateway` 运行环境为 `AUCTION_AI_PROVIDER=deepseek`，并非 search gate 要求的 mock-AI 模式，因此即使镜像重建，也仍需在隔离项目中以 mock 模式重启 Gateway 后再取证。
- Kubernetes source/release 静态 gate 中，`test/infra/prod-release-images.test.sh` 与 `test/infra/l2-ha-fault-drill.test.sh` 已通过；`scripts/check-kubernetes-manifests.sh` 因当前容器化 Go 环境无法解析 `proxy.golang.org` 而阻塞。`kubectl` 客户端存在，但 `current-context` 为空，当前会话仍没有真实集群 rollout/chaos 证据。

## 完成前必须执行

当前本地 `main` 显著超前 `origin/main`；稳定的远端基线为 `8d13a9cb4eacd1e6191f6dc33c606008f41e42ae`。本机没有 `gh`，已连接的 GitHub App 查询 `Ye-yellow/auction-backend` Actions 返回 `404 Not Found`，因此当前没有任何远端 CI 结果可以证明这些本地提交；在获得 push 与仓库 Actions 读取授权前，所有通过项都只按本地自动化证据表述。

1. 经所有者单独授权后扫描 Git 历史，轮换任何曾提交凭据；只有确认需要时才制定历史清理和 force-push 方案。
2. 经所有者授权将本地提交发布到受保护分支，在远端执行完整 CI，并归档 run URL、commit SHA、各 job 结果与集成测试 artifact。
3. 在目标 Linux/Kubernetes 环境复验 L2 smoke 和 HA drill 并归档日志；两项已在本地 Docker Desktop 通过。
4. 在目标规格环境完成 5000 请求单热拍品突发、单热拍品持续 `100 req/s`、双热拍品重叠窗口聚合 `≥200 req/s`、2 万 WebSocket、断线重连、Projector 追平，以及 mock-AI 条件下 5000 次 ES/pgvector 双索引检索各自 p99 `< 1000ms`。
5. 执行 Kafka 磁盘写满、MySQL failover、ES master kill、Relay/Projector kill、投影闸门关闭且取消/落槌持续可用及 3 次健康采样恢复、synthetic repair 随机 offset kill+同 bundle 续跑、NATS 分区与单 AZ 故障演练。
6. 由业务负责人签署 R3 的耐久性权衡；若不接受，架构必须改为 Kafka command-first。

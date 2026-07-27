# 直播互动竞拍系统后端

当前阶段：V1 核心业务闭环 + 基础设施主路径落地。

## 项目最强纲领

最高需求基线是用户提供的《抖音电商AI全栈课题-直播竞拍全栈系统（宣讲版）》PDF。后续产品、架构、后端、前端、测试和答辩都必须按该课题评分点推进，不再自由发挥。

必须闭合主流程：商品上架 → 规则配置 → 主播开拍 → 用户实时出价 → WebSocket 同步价格/排名/倒计时 → 自动延时/封顶价自动成交/主播异常取消 → 竞拍结束 → 自动生成订单 → 用户查看结果/模拟支付。

P0 硬点：0 元起拍、固定加价、封顶价自动成交、10-30 秒自动延时、异常取消、订单管理、移动 H5 观众端、WebSocket 心跳重连/快照恢复、被超越/领先/延时/结束提醒、100+ 并发出价一致性。

端划分说明：后端仍提供完整竞拍、出价、用户、WebSocket 契约；但当前兄弟前端 `live-auction-bid-frontend` 只作为商家/主播/运营后台 Web。用户 H5 竞拍端和小程序端后续应作为独立项目接入同一后端契约，不在当前后台前端仓库中实现入口或页面。

工程铁律和完整纲领见根文档：[`PROJECT_CHARTER.md`](./PROJECT_CHARTER.md)。

## 已落地的主链路

- 核心业务：创建拍品、开拍、出价、排行榜、信任卡揭示、Duel、落锤、快照、WebSocket 事件广播。
- 数据主路径：MySQL/GORM 保存管理态与已确认业务投影；竞拍期间 Redis 保存权威运行态、排行榜、幂等记录、到期索引和原子 List Outbox，Kafka Projector 异步落库。Redis 不可用时拒绝裁决，不回退到 MySQL 直写运行态。
- 本地基础设施：Docker Compose 独立启动 gateway、auction-service、outbox-relay、projector、domain-relay、enrichment-consumer、index-es、index-pgvector、close-worker、MySQL、Redis、Kafka、NATS 等目标依赖；检索消费者通过 `search` profile 启用。
- 服务边界：gateway 只对外提供 HTTP/WebSocket；开拍、出价、信任卡揭示、对决、落槌、取消等直播命令全部经私有 gRPC 同步调用 auction-service。开拍、出价、落槌、取消由 Redis Lua 原子裁决；信任卡和对决写独立展示态版本，不推进运行态版本。开拍会在 MySQL 拍品行锁内重新读取配置并执行短 Redis 命令，草稿修改和排队持同一行锁且先确认 runtime 不存在，避免旧配置开拍与异步投影版本分叉。反狙击延时属于出价 Lua 的同一次裁决，不存在独立 RPC 竞态。gateway 不保留本地命令 fallback，缺少或无法连接 auction-service 时拒绝就绪。
- 服务发现：本地使用 Docker DNS，生产使用 Kubernetes Service DNS；gateway 通过 `auction-service:19090` 访问无状态 auction-service 副本，不维护静态实例清单或房间所有权。auction-service 使用独立的 `:18086/readyz` 运维探针。
- 事件流：生命周期和出价由 Redis Lua 原子裁决并把运行事实写入 Redis List Outbox；独立 Relay 投递 Kafka，Projector 以 inbox + DB offset 事务投影 MySQL，再由 MySQL Domain Outbox 发布领域事件；订单 enrichment 消费者按 `message_id + payload_hash` 幂等补全地址和店铺快照。
- 向量检索写入：`index-pgvector` 独立消费 `auction.lot.state.v1`，按 `lot_version + last_event_id + content_hash` 拒绝同版本冲突；价格和状态变化复用既有向量，只有稳定检索内容或模型身份变化才重新调用 Embedding。gateway 只读 pgvector，不再轮询 MySQL 或执行搜索 DDL。
- 关键词检索写入：`index-es` 使用独立 Kafka group 消费同一 LotState 事件，通过 `version_type=external` 严格按 `lot_version` 写入 `auction-lots-current`；旧版本跳过、完全重复确认后提交、同版本不同事件身份进入 DLQ。索引和 alias 由部署任务初始化，应用启动不会创建或修改 mapping。
- 混合检索读取：gateway 并行执行 Elasticsearch 关键词召回与 pgvector 语义召回，用 RRF 融合候选；最终以一次 MySQL 批量读取和一次 Redis pipeline 覆盖运行态，再执行公开性、状态和房间过滤。任一检索器故障时使用另一侧，两侧不可用或权威回源失败时降级到 MySQL 过滤路径。
- 检索重建：MySQL 管理态的创建、编辑、排队与 Redis 运行态共用事务性 Domain Outbox 发布完整 `lot.state`；`auction-search-rebuild` 按 Kafka 水位 + MySQL 一致性快照重建版本化 ES 索引或 pgvector 表。ES 原子切换 alias；pgvector 优先复用相同 hash 的旧向量，在付费调用硬上限内补齐后原子换表。操作步骤见 [`docs/SEARCH_REBUILD_RUNBOOK.md`](docs/SEARCH_REBUILD_RUNBOOK.md)。
- 检索对账：`search-reconciler` 按主键游标持续抽样 MySQL 与 ES/pgvector 的 `lot_version + last_event_id + content_hash`；缺失或落后时仅重发已验证的原始 `lot.state` Kafka 记录，让两个独立消费者自行幂等修复；同版本分叉或索引超前只写 P0 finding 并告警，绝不强制覆盖。操作步骤见 [`docs/SEARCH_RECONCILIATION_RUNBOOK.md`](docs/SEARCH_RECONCILIATION_RUNBOOK.md)。
- 用户系统：自建 username/password 账号，用户 ID 使用雪花字符串；JWT access token + refresh session 支持注册、登录、刷新、登出、me 和 RBAC 团队账号管理。
- 鉴权权限：公开读接口可匿名访问；出价走 `bid.place`，后台操作按 `lot.*`、`auction.control`、`order.manage`、`team.user.*` 等权限码判断，不再使用旧角色枚举做运行时鉴权。
- 健康检查：`/healthz` 存活检查，`/readyz` 检查本进程所需依赖和 Redis generation 安全状态；Relay、Projector、Domain Relay 各自提供独立探针。
- 统一响应：service 对外只用 reply.result 表达业务成功/失败；Go `error` 不再承载可预期业务错误，避免前端同时解析 body 和 transport error。

被替代的 `MemoryStore`、`database/sql` repo、手写 `schema.go` 主路径已删除，不保留 fallback 或双实现开关。

## 本地运行

```bash
cd deploy
cp .env.example .env
# 编辑 deploy/.env，填写 TOS endpoint、region、bucket、AK 和 SK。
docker compose up --build
```

Compose 会先运行一次性 `migrate` 服务；只有全部版本化迁移成功后，`auction-service` 和 `gateway` 才会启动。应用进程只读校验迁移版本、名称和校验和，不会在启动时修改表结构。

gateway 默认监听 `http://127.0.0.1:18080`；auction-service 的 gRPC `:19090` 只在 Compose 内网暴露，宿主机仅映射运维探针 `http://127.0.0.1:18086`。本地、性能三副本和分片验证 Compose 都使用同一拆分边界，不再启动兼容单体。

后端默认使用 TOS 作为图片存储。Docker Compose 启动前必须复制 `deploy/.env.example` 为 `deploy/.env` 并填写 TOS 配置；如果 `AUCTION_STORAGE_PROVIDER=tos` 且 endpoint、region、bucket、access key、secret key 任一缺失，Compose 或后端会 fail fast，不会等到 upload 接口才返回 `storage not configured`。

Docker Compose 默认创建本地主账号：

```text
username: main
password: main_dev_password
```

常用检查：

```bash
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
curl http://127.0.0.1:18086/readyz
```

### 目标数据库 migration

重构目标库由独立、带版本和校验和的 migrate 命令创建，不接受旧 volume 原地升级：

```bash
export AUCTION_MYSQL_DSN='auction:password@tcp(127.0.0.1:3306)/live_auction?charset=utf8mb4'
go run ./app/auction/service/cmd/migrate up
go run ./app/auction/service/cmd/migrate status
go run ./app/auction/service/cmd/migrate --steps 1 down
```

目标 DDL 位于 [`deploy/mysql/migrations`](./deploy/mysql/migrations/README.md)。旧的日期前缀 SQL 仅保留为历史资料，不会被 migrate 命令加载。

L3 运行态已切到 Redis List Outbox → Kafka → Projector，L4b 订单补全由独立 enrichment-consumer 承担。MySQL DDL 只允许通过独立 migrate 命令执行；数据库缺少迁移、存在版本断层、未知版本或校验和漂移时，gateway、auction-service、projector、domain-relay 与 enrichment-consumer 都会拒绝启动。

### 火山引擎 TOS 图片上传配置

前端添加拍品页统一调用 `POST /api/uploads/images`，后端通过 `StorageProvider` 接火山引擎 TOS。AK/SK 只放运行环境变量，不进入前端或仓库。

本地 Docker Compose 必须复制模板后填写 TOS 配置：

```bash
cd deploy
cp .env.example .env
# 编辑 deploy/.env，填入 AUCTION_TOS_ENDPOINT / AUCTION_TOS_REGION / AUCTION_TOS_BUCKET / AUCTION_TOS_ACCESS_KEY / AUCTION_TOS_SECRET_KEY
```

配置示例只展示字段名，真实值只放本地 `deploy/.env`：

```env
AUCTION_STORAGE_PROVIDER=tos
AUCTION_TOS_ENDPOINT=<tos-endpoint>
AUCTION_TOS_REGION=<tos-region>
AUCTION_TOS_BUCKET=<tos-bucket>
AUCTION_TOS_PUBLIC_BASE_URL=<public-base-url>
AUCTION_TOS_USE_SSL=true
AUCTION_TOS_ACCESS_KEY=<tos-access-key>
AUCTION_TOS_SECRET_KEY=<tos-secret-key>
```

上传接口会：校验权限与图片类型 → 生成 `{bizType}/{yyyy}/{mm}/{assetId}.{ext}` 对象键 → 上传 TOS → 写入 `asset_files` → 返回 `asset.imageUrl` 给前端预览和 `createLot.imageUrl` 使用。

用户与出价示例：

```bash
curl -X POST http://127.0.0.1:18080/api/users/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"buyer1","password":"password123","nickname":"买家一号"}'

curl -X POST http://127.0.0.1:18080/api/lots/{lot_id}/bid \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <access_token>' \
  -d '{"amount":{"amount":11000,"currency":"CNY"},"idempotency_key":"buyer1-11000"}'
```

### 公开直播间可见性规则

`GET /api/rooms` 只返回真实可进入的公开直播间：

- room 必须是 `ACTIVE`。
- room 必须至少有 1 个拍品处于 `QUEUED`、`LIVE` 或 `EXTENDED`。
- 没有拍品，或拍品只处于 `DRAFT`、`READY`、`SETTLED`、`CANCELLED`、`FAILED` 时，不对 H5 公开。
- `/api/admin/rooms` 不受这个过滤影响，仍然返回主账号自己的后台房间列表。

该规则由 `RoomQuery.PublicVisibleOnly` 承载，避免改 proto/schema。

### 实时排行榜 TopN

Redis 仍保存完整竞价排行榜；实时热路径只返回 TopN，避免高并发下每次出价/快照/WS 事件拉全量 ranking。

```env
AUCTION_REALTIME_RANKING_LIMIT=50
```

未设置或设置为非法值时默认 `50`。完整出价历史仍通过订单/出价历史等非热路径分页接口查询。

### 并发出价烟测

需要先启动后端及 MySQL/Redis，然后执行：

```bash
PROJECTOR_METRICS_URL=http://127.0.0.1:18083/metrics \
RELAY_METRICS_URL=http://127.0.0.1:18082/metrics \
CONCURRENCY=100 node scripts/load-bid-hot-path.mjs
```

Projector、Outbox Relay 与 Gateway 是独立部署单元，脚本强制分别抓取三者的指标；缺少独立 worker 指标或误把 Gateway 指标当投影指标时直接失败，避免把不存在的时间序列当作零积压。

如果本机配置了 HTTP 代理，建议显式绕过 localhost：

```bash
NO_PROXY=127.0.0.1,localhost CONCURRENCY=100 node scripts/load-bid-hot-path.mjs
```

可选参数：

```env
BASE_URL=http://127.0.0.1:18080
GATEWAY_METRICS_URL=http://127.0.0.1:18080/metrics
PROJECTOR_METRICS_URL=http://127.0.0.1:18083/metrics
RELAY_METRICS_URL=http://127.0.0.1:18082/metrics
AUCTION_REALTIME_RANKING_LIMIT=50
MERCHANT_USERNAME=
MERCHANT_PASSWORD=
RUN_ID=
```

脚本会创建/复用商家，创建拍品并排队开拍，确认 `/api/rooms` 可见，注册 N 个买家并并发出价，最后报告 `total/accepted/rejected/errors/P50/P95/P99/finalPrice/leader/rankingLength`。它同时从 Relay 等待 Redis Outbox pending/inflight 清空、从 Projector 等待 Kafka lag 清空，并断言成功 ACK 数覆盖本轮有效出价、没有 ACK 队列不变量错误、Projector 未暂停。最终价与领先者必须来自最高有效出价，ranking 已排序且不超过 TopN，幂等重放不得重复生成出价，封顶成交时买家订单只出现一次。

没有本地 MySQL/Redis/HTTP 服务时，可以先跑业务层并发一致性烟测：

```bash
go test ./app/auction/service/test -run TestConcurrentBidSmokeMaintainsLeaderRankingLimitIdempotencyAndCapOrder -count=1 -v
```

该用例固定 100 个买家并发出价到封顶价，验证公开房间可见、最终成交价/领先者、实时榜 TopN、幂等重放不重复入库、封顶订单只创建一次。它不能替代上面的 Redis Lua HTTP 压测。

公共注册和重置密码保持当前 demo 友好策略，后端不在这一阶段收紧。

如果宿主机安装了 Go：

```bash
go test ./...
go build ./app/auction/service/cmd/server
go build ./app/auction/service/cmd/auction-service
```

HTTP 黑盒契约测试位于 `test/e2e`，默认未设置后端地址时会跳过：

```bash
go test ./test/e2e
LIVE_AUCTION_E2E_BASE_URL=http://127.0.0.1:18080 go test ./test/e2e -count=1 -v
```

详细说明见 [`docs/infra/e2e-contract-tests.md`](docs/infra/e2e-contract-tests.md)。

故障演练不得以“容器重新 healthy”代替数据正确性证明；`scripts/run-fault-injection.sh` 的 before/during/after 业务断言契约见 [`docs/infra/fault-injection.md`](docs/infra/fault-injection.md)。

## 文档

主要文档位于 `docs/openclaw/v1/`。


### 统一响应与乐观锁冲突语义

当多实例或并发请求触发 lot expected-version 冲突时，data 层统一返回稳定哨兵错误，service 层包装进 reply.result：`code=409001`，`message=lot state changed, please refresh and retry`。前端应刷新拍品快照后提示用户重试，不应按普通 500 处理。

### 工程设计原则

- **统一返回模式（Result Envelope）**：service 层把可预期业务错误收敛到 `ReplyResult`，对前端形成单一解析入口；transport error 只留给不可包装的系统/链路故障。
- **Repository + Unit of Work**：data 层持有 GORM/Redis/事务边界，lot/bid/event 在必要场景进入同一个 MySQL transaction，biz 层只依赖 repo 接口。
- **双 Outbox**：Redis Lua 与运行状态同原子操作写 List Outbox，Relay 至少一次投递 Kafka；Projector 与 MySQL 业务投影同事务写 Domain Outbox，消费侧按全局 event ID 幂等。
- **Platform Discovery + Health Check**：本地 Docker DNS、生产 Kubernetes Service DNS 负责发现；server 层只聚合本进程安全依赖到 `/readyz`，不让治理基础设施进入 biz。
- **测试隔离**：跨层业务测试保留在 `app/auction/service/test`；HTTP 黑盒契约测试放在 `test/e2e`；同包内部测试只在确需覆盖私有转换、worker、repo、hub 等内部行为时保留。

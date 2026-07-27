# Backend E2E Contract Tests

## 目的

`test/e2e` 是后端 HTTP 黑盒契约测试目录。它不 import `app/auction/service/internal/...`，只通过真实 HTTP API 验证前后端共同依赖的稳定契约：

- HTTP transport 状态和业务 `result` 外壳。
- 注册、登录、刷新、登出、重置密码。
- RBAC 团队账号管理、角色矩阵、跨商家隔离和买家越权保护。
- 拍品开拍、出价鉴权、幂等键、业务拒绝码和控制台状态流转。
- 草稿自动保存、入队、重复入队、非法商品资料和非法竞拍规则拒绝。
- 迁移到 proto 生成 handler 后的业务 HTTP route，包括商品、地址、商城订单、统一订单、后台/公开列表、买家推荐。

这些测试补的是“服务启动后的对外合同”，不是包内单元测试。领域状态机、repo 转换、Redis/MySQL 投影、WebSocket hub 等同包测试仍保留在原包或 `app/auction/service/test`。

## 目录说明

| 文件 | 覆盖内容 |
| --- | --- |
| `test/e2e/client_test.go` | HTTP client、唯一账号、token、`result` 断言、JSON helper。 |
| `test/e2e/http_contract_test.go` | `/healthz`、未登录 `result`、trace/request header、非法 JSON、CORS preflight、缺失资源 `result` 外壳。 |
| `test/e2e/auth_contract_test.go` | 注册登录、`/api/users/me`、refresh 轮换、logout、重置密码、注册参数校验、登录失败、用户名规范化、伪造 bearer token。 |
| `test/e2e/rbac_contract_test.go` | 商家主账号创建团队用户、角色白名单/黑名单、买家访问 admin 被拒、跨商家隔离、团队用户改角色/重置密码/禁用启用、列表过滤和分页。 |
| `test/e2e/auction_auth_contract_test.go` | 拍品操作鉴权、出价使用 token 身份、幂等重放、缺幂等键、未开拍/取消后出价、太低出价、币种不一致、领先者重复加价、非法状态流转。 |
| `test/e2e/auction_draft_queue_contract_test.go` | 草稿创建、静默保存、详情字段保存、入队、重复入队、标题/图片/规则/最低加价/时长/币种/封顶价/图片 URL 校验、非法 autosave 校验。 |
| `test/e2e/proto_migration_contract_test.go` | 手写业务 HTTP route 迁到 proto 后的 smoke/contract：商品、地址、商城订单、统一订单、后台列表、公开房间、买家推荐。 |

## 当前测试矩阵

| 功能组 | 场景设计 | 主要覆盖 |
| --- | --- | --- |
| `http_contract` | 4 个测试函数，覆盖 6 类传输契约 | 健康检查、统一 `result`、trace/request header 透传、非法 JSON、CORS、缺失资源错误码。 |
| `auth_contract` | 5 个测试函数，覆盖约 20 条认证场景 | 注册、登录、用户名大小写/空格归一化、重复用户名、重置密码、旧密码失效、refresh token 轮换/重放失败、logout 幂等、伪造 bearer token。 |
| `rbac_contract` | 4 个测试函数，覆盖约 22 条权限场景 | anchor/operator 可创建，buyer/merchant_owner/空角色不可创建，未登录/买家越权，跨商家隔离，团队用户角色变更、短密码拒绝、重置密码、禁用启用、列表过滤、空结果、分页归一化。 |
| `auction_auth_contract` | 5 个测试函数，覆盖约 24 条竞拍场景 | 创建/出价/开拍权限，买家身份从 token 注入，押金地址前置，押金幂等，未支付押金拒绝，出价幂等重放，缺幂等键，领先者重复加价，低价拒绝，币种拒绝，未开拍/取消后拒绝，重复开拍/无出价落锤/无原因取消。 |
| `auction_draft_queue_contract` | 4 个测试函数，覆盖约 21 条草稿队列场景 | 空草稿、autosave 补全、详情字段保存、入队、未登录/买家越权、重复入队幂等，缺标题/缺图片/缺规则/最低加价为 0/时长不足/币种不一致/封顶价错误/图片 URL 错误，非法主图/轮播图过多/库存非法/保证金币种缺失。 |
| `proto_migration_contract` | 2 个测试函数，覆盖约 22 条迁移 route 场景 | `shop.proto` 商品/地址/商城订单/统一订单支付，`auction.proto` 拍品结果/买家订单和出价/后台订单和拍品和房间/公开房间/买家推荐，覆盖未登录、越权、参数错误、缺失资源和分页结果。 |

## 用例明细

当前情况列说明：

- `已实现；真实 HTTP 已通过`：测试代码已落在 `test/e2e`，并已在 Docker Compose 启动的后端 `http://127.0.0.1:18080` 上跑通。
- 普通 `go test ./...` 仍会跳过 e2e 的真实 HTTP 请求；只有设置 `LIVE_AUCTION_E2E_BASE_URL` 时才会打真实服务。

### HTTP Contract Cases

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| HTTP-01 healthz | `GET /healthz`，无 token | HTTP 200，响应 `ok=true` | 确认后端健康检查可被部署/探针稳定识别。 | 已实现；真实 HTTP 已通过 |
| HTTP-02 unauth result envelope | `GET /api/users/me`，无 token，带 `X-Request-Id=e2e-request-id`、`X-Trace-Id=e2e-trace-id` | HTTP 200，`result.code=401001`，`message=login required`，trace/request header 回写 | 确认未登录错误仍使用业务 `result` 外壳，并保留链路追踪字段。 | 已实现；真实 HTTP 已通过 |
| HTTP-03 invalid JSON | `POST /api/shop/addresses`，body 为 `{` | HTTP 200，`result.code=400001` | 确认非法 JSON 不暴露框架错误，统一映射参数错误。 | 已实现；真实 HTTP 已通过 |
| HTTP-04 CORS preflight | `OPTIONS /api/users/me`，`Origin=http://localhost:5173`，请求 `Authorization,Content-Type` | HTTP 204，返回 `Access-Control-Allow-Origin` 和允许请求头 | 确认本地前端调试跨域预检可通过。 | 已实现；真实 HTTP 已通过 |
| HTTP-05 missing resource envelope | `GET /api/lots/missing-lot/result` | HTTP 200，`result.code=404001`，包含 `result` 对象 | 确认缺失资源也不破坏统一响应格式。 | 已实现；真实 HTTP 已通过 |

### Auth Contract Cases

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| AUTH-01 register buyer | `POST /api/users/register`，唯一用户名、合法密码、昵称 | HTTP 200，`result.code=0`，返回 `user.id`、access token、refresh token | 确认买家公开注册可建立有效登录态。 | 已实现；真实 HTTP 已通过 |
| AUTH-02 get me | `GET /api/users/me`，使用 AUTH-01 access token | HTTP 200，`result.code=0`，`user.username` 等于注册用户名 | 确认 access token 能读取当前用户身份。 | 已实现；真实 HTTP 已通过 |
| AUTH-03 wrong password in main chain | `POST /api/users/login`，正确用户名、错误密码 | HTTP 200，`result.code=401005` | 确认错误密码不会登录成功。 | 已实现；真实 HTTP 已通过 |
| AUTH-04 refresh token rotation | `POST /api/users/refresh`，使用注册返回 refresh token | HTTP 200，`result.code=0`，返回新 refresh token，且不同于旧 token | 确认 refresh token 轮换契约稳定。 | 已实现；真实 HTTP 已通过 |
| AUTH-05 old refresh replay | 再次 `POST /api/users/refresh`，使用旧 refresh token | HTTP 200，`result.code=401004` | 确认已轮换 refresh token 不能重放。 | 已实现；真实 HTTP 已通过 |
| AUTH-06 logout | `POST /api/users/logout`，使用当前 refresh token | HTTP 200，`result.code=0` | 确认 logout 可撤销当前 refresh session。 | 已实现；真实 HTTP 已通过 |
| AUTH-07 refresh after logout | logout 后再次 refresh 同一 token | HTTP 200，`result.code=401004` | 确认登出后 refresh session 失效。 | 已实现；真实 HTTP 已通过 |
| AUTH-08 duplicate username | 注册已存在用户名 | HTTP 200，`result.code=409002` | 确认用户名唯一约束对外业务码稳定。 | 已实现；真实 HTTP 已通过 |
| AUTH-09 reset password | `POST /api/users/reset-password`，已有用户名、新合法密码 | HTTP 200，`result.code=0` | 确认原型阶段重置密码入口可用。 | 已实现；真实 HTTP 已通过 |
| AUTH-10 old password rejected | 重置后用旧密码登录 | HTTP 200，`result.code=401005` | 确认重置密码会让旧凭据失效。 | 已实现；真实 HTTP 已通过 |
| AUTH-11 new password login | 重置后用新密码登录 | HTTP 200，`result.code=0`，返回新 token | 确认用户能用新凭据恢复登录。 | 已实现；真实 HTTP 已通过 |
| AUTH-12 buyer username too short | 注册 `username=abc`、合法密码、昵称 | HTTP 200，`result.code=400001` | 确认买家注册用户名长度下限。 | 已实现；真实 HTTP 已通过 |
| AUTH-13 unsupported username char | 注册用户名包含 `$` | HTTP 200，`result.code=400001` | 确认用户名字符集限制。 | 已实现；真实 HTTP 已通过 |
| AUTH-14 short password | 注册密码为 `short` | HTTP 200，`result.code=400001` | 确认密码长度策略。 | 已实现；真实 HTTP 已通过 |
| AUTH-15 blank nickname | 注册昵称为空白字符串 | HTTP 200，`result.code=400001` | 确认昵称必填。 | 已实现；真实 HTTP 已通过 |
| AUTH-16 unknown login username | 登录不存在用户名，合法密码 | HTTP 200，`result.code=401005` | 确认登录失败不泄露账号是否存在。 | 已实现；真实 HTTP 已通过 |
| AUTH-17 blank login username | 登录用户名为空白字符串 | HTTP 200，`result.code=401005` | 确认空用户名按无效凭据处理。 | 已实现；真实 HTTP 已通过 |
| AUTH-18 blank login password | 登录密码为空字符串 | HTTP 200，`result.code=401005` | 确认空密码按无效凭据处理。 | 已实现；真实 HTTP 已通过 |
| AUTH-19 username normalization | 登录用户名带前后空格并转大写，密码正确 | HTTP 200，`result.code=0`，返回归一化后用户名 | 确认用户名 trim + lower 归一化不影响登录。 | 已实现；真实 HTTP 已通过 |
| AUTH-20 empty refresh token | `POST /api/users/refresh`，`refresh_token=""` | HTTP 200，`result.code=400001` | 确认 refresh 空值是参数错误。 | 已实现；真实 HTTP 已通过 |
| AUTH-21 unknown refresh token | `POST /api/users/refresh`，伪造 refresh token | HTTP 200，`result.code=401004` | 确认未知 refresh 按 session 过期处理。 | 已实现；真实 HTTP 已通过 |
| AUTH-22 empty logout token | `POST /api/users/logout`，`refresh_token=""` | HTTP 200，`result.code=0` | 确认空 logout 是幂等安全操作。 | 已实现；真实 HTTP 已通过 |
| AUTH-23 invalid bearer token | `GET /api/users/me`，`Authorization: Bearer not-a-jwt` | HTTP 200，`result.code=401003` | 确认 access token 格式错误与未登录区分。 | 已实现；真实 HTTP 已通过 |

### RBAC Contract Cases

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| RBAC-01 create anchor | 商家主账号 `POST /api/admin/users`，`role_code=anchor` | HTTP 200，`result.code=0`，用户角色包含 `anchor`，`mainAccountId` 为商家 ID | 确认主账号可创建主播子账号且归属正确。 | 已实现；真实 HTTP 已通过 |
| RBAC-02 create operator | 商家主账号创建 `role_code=operator` | HTTP 200，`result.code=0`，用户角色包含 `operator` | 确认主账号可创建运营子账号。 | 已实现；真实 HTTP 已通过 |
| RBAC-03 reject buyer subaccount | 商家主账号创建 `role_code=buyer` | HTTP 200，`result.code=400001` | 确认 admin 创建入口不能创建买家。 | 已实现；真实 HTTP 已通过 |
| RBAC-04 reject merchant owner subaccount | 商家主账号创建 `role_code=merchant_owner` | HTTP 200，`result.code=400001` | 确认 admin 创建入口不能创建新的主账号。 | 已实现；真实 HTTP 已通过 |
| RBAC-05 reject blank role | 商家主账号创建空 `role_code` | HTTP 200，`result.code=400001` | 确认角色必填且必须在团队角色白名单内。 | 已实现；真实 HTTP 已通过 |
| RBAC-06 list created users | 商家主账号 `GET /api/admin/users?page=1&pageSize=20` | HTTP 200，`result.code=0`，`total` 至少包含本测试创建的团队用户 | 确认创建后的团队用户可被主账号列表读取。 | 已实现；真实 HTTP 已通过 |
| RBAC-07 admin list without token | 无 token `GET /api/admin/users` | HTTP 200，`result.code=401001` | 确认后台列表需要登录。 | 已实现；真实 HTTP 已通过 |
| RBAC-08 buyer admin list forbidden | 买家 token `GET /api/admin/users` | HTTP 200，`result.code=403001` | 确认买家不能访问后台团队管理。 | 已实现；真实 HTTP 已通过 |
| RBAC-09 cross merchant role update | 其他商家 token 修改本商家团队用户角色 | HTTP 200，`result.code=403001` | 确认团队用户管理按 `mainAccountId` 隔离。 | 已实现；真实 HTTP 已通过 |
| RBAC-10 reject role update to buyer | 本商家主账号把团队用户改为 `buyer` | HTTP 200，`result.code=400001` | 确认团队账号不能被改成买家角色。 | 已实现；真实 HTTP 已通过 |
| RBAC-11 update role to operator | 本商家主账号把 anchor 改成 operator | HTTP 200，`result.code=0`，角色包含 `operator` | 确认合法角色变更生效。 | 已实现；真实 HTTP 已通过 |
| RBAC-12 reject short reset password | 重置团队用户密码为 `short` | HTTP 200，`result.code=400001` | 确认后台重置密码也执行密码策略。 | 已实现；真实 HTTP 已通过 |
| RBAC-13 reset team password | 重置团队用户密码为 `newteampass123` | HTTP 200，`result.code=0` | 确认主账号可重置团队用户密码。 | 已实现；真实 HTTP 已通过 |
| RBAC-14 disable team user | `POST /api/admin/users/{id}/status`，`status=2` | HTTP 200，`result.code=0`，`user.status=USER_STATUS_DISABLED` | 确认主账号可禁用团队用户。 | 已实现；真实 HTTP 已通过 |
| RBAC-15 disabled login rejected | 被禁用团队用户登录 | HTTP 200，`result.code=403002` | 确认禁用账号不能登录。 | 已实现；真实 HTTP 已通过 |
| RBAC-16 enable team user | `POST /api/admin/users/{id}/status`，`status=1` | HTTP 200，`result.code=0` | 确认主账号可恢复团队用户。 | 已实现；真实 HTTP 已通过 |
| RBAC-17 enabled login succeeds | 恢复后团队用户用新密码登录 | HTTP 200，`result.code=0` | 确认启用后登录能力恢复。 | 已实现；真实 HTTP 已通过 |
| RBAC-18 role pagination filter | `GET /api/admin/users?page=1&pageSize=1&roleCode=anchor` | HTTP 200，`result.code=0`，`page=1`，`pageSize=1`，返回用户数不超过 1 | 确认分页和角色过滤契约。 | 已实现；真实 HTTP 已通过 |
| RBAC-19 invalid role filter | `GET /api/admin/users?roleCode=buyer` | HTTP 200，`result.code=400001` | 确认团队用户列表不能用 buyer 角色过滤。 | 已实现；真实 HTTP 已通过 |
| RBAC-20 active status filter | `GET /api/admin/users?status=USER_STATUS_ACTIVE&page=1&pageSize=5` | HTTP 200，`result.code=0`，`total>=1` | 确认状态过滤可用于查 active 团队账号。 | 已实现；真实 HTTP 已通过 |
| RBAC-21 keyword empty result | `GET /api/admin/users?keyword=<不存在关键字>&page=1&pageSize=5` | HTTP 200，`result.code=0`，`total=0`，`users=[]` | 确认搜索无结果时返回空列表而不是错误或泄露其它商家数据。 | 已实现；真实 HTTP 已通过 |
| RBAC-22 pagination normalization | `GET /api/admin/users?page=0&pageSize=200` | HTTP 200，`result.code=0`，`page=1`，`pageSize=100` | 确认非法页码和超大 pageSize 会被归一化到稳定边界。 | 已实现；真实 HTTP 已通过 |

### Auction Contract Cases

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| AUCT-01 unauth create lot | 无 token `POST /api/lots`，合法拍品 body | HTTP 200，`result.code=401001` | 确认创建拍品需要登录。 | 已实现；真实 HTTP 已通过 |
| AUCT-02 buyer create lot forbidden | 买家 token `POST /api/lots` | HTTP 200，`result.code=403001` | 确认买家不能创建拍品。 | 已实现；真实 HTTP 已通过 |
| AUCT-03 unauth bid | 无 token `POST /api/lots/missing-lot/bid` | HTTP 200，`result.code=401001` | 确认出价需要登录。 | 已实现；真实 HTTP 已通过 |
| AUCT-04 merchant bid forbidden | 商家主账号 token 出价 | HTTP 200，`result.code=403001` | 确认商家/后台账号不能走买家出价权限。 | 已实现；真实 HTTP 已通过 |
| AUCT-05 buyer start forbidden | 买家 token `POST /api/lots/missing-lot/start` | HTTP 200，`result.code=403001` | 确认买家不能开拍。 | 已实现；真实 HTTP 已通过 |
| AUCT-06 first bid accepted | 草稿补全并开拍后，买家先创建收货地址并 `mock-pay` 押金占用，再出价 `11000 CNY`，带幂等键 | HTTP 200，`result.code=0`，`accepted=true`，返回 `bid` | 确认买家满足押金前置后可对 live 拍品正常出价。 | 已实现；真实 HTTP 已通过 |
| AUCT-07 bid identity from token | 出价 body 不传 user，使用买家 token | 返回 `bid.userId` 等于 token 中用户 ID | 确认出价身份不能由请求 body 伪造。 | 已实现；真实 HTTP 已通过 |
| AUCT-08 idempotent bid replay | 同一买家、同一 lot、同一幂等键再次出价 | HTTP 200，`result.code=0`，`accepted=true`，返回同一 bid id | 确认客户端重试不会重复记账。 | 已实现；真实 HTTP 已通过 |
| AUCT-09 missing idempotency key | 买家出价只传金额，不传 `idempotency_key` | HTTP 200，`result.code=400001`，`accepted=false` | 确认出价必须带幂等键。 | 已实现；真实 HTTP 已通过 |
| AUCT-10 leading bidder repeat | 当前领先买家立刻再次出价 | HTTP 200，`result.code=409104`，`accepted=false` | 确认领先者不能自己连续加价。 | 已实现；真实 HTTP 已通过 |
| AUCT-11 bid too low | 其他买家先完成地址和押金占用，再出价 `11500 CNY`，低于当前价 + 最低加价 | HTTP 200，`result.code=409101`，`accepted=false` | 确认押金前置满足后，最低加价规则仍稳定生效。 | 已实现；真实 HTTP 已通过 |
| AUCT-12 currency mismatch | 其他买家先完成地址和押金占用，再出价 `13000 USD` | HTTP 200，`result.code=409105`，`accepted=false` | 确认押金前置满足后，出价币种必须与拍品规则一致。 | 已实现；真实 HTTP 已通过 |
| AUCT-13 bid before live | 买家先对已补全但未开拍的草稿完成地址和押金占用，再出价 | HTTP 200，`result.code=409102` | 确认未开拍不能出价，且错误码不会被押金前置遮蔽。 | 已实现；真实 HTTP 已通过 |
| AUCT-14 cancel live lot | 商家对 live 拍品 `POST /api/lots/{id}/cancel`，带原因 | HTTP 200，`result.code=0` | 确认主播/商家可异常取消 live 拍品。 | 已实现；真实 HTTP 已通过 |
| AUCT-15 bid after cancel | 买家对 live 拍品先完成地址和押金占用，商家取消后再次出价 | HTTP 200，`result.code=409107` | 确认取消后拍品不再接受出价，并验证 runtime 快路径也映射为取消错误码。 | 已实现；真实 HTTP 已通过 |
| AUCT-16 start live lot again | 对已开拍拍品再次 `POST /start` | HTTP 200，`result.code=400001` | 确认重复开拍是非法状态流转。 | 已实现；真实 HTTP 已通过 |
| AUCT-17 settle without bid | 对 live 但无有效出价拍品 `POST /settle` | HTTP 200，`result.code=400001` | 确认无成交出价时不能落锤成交。 | 已实现；真实 HTTP 已通过 |
| AUCT-18 cancel without reason | 对 live 拍品取消但不传原因 | HTTP 200，`result.code=400001` | 确认取消原因必填。 | 已实现；真实 HTTP 已通过 |
| AUCT-19 unauth deposit hold | 无 token `POST /api/lots/{id}/deposit-holds/mock-pay` | HTTP 200，`result.code=401001` | 确认押金占用也必须登录。 | 已实现；真实 HTTP 已通过 |
| AUCT-20 merchant deposit hold forbidden | 商家主账号 token 创建押金占用 | HTTP 200，`result.code=403001` | 确认后台/商家账号不能走买家押金链路。 | 已实现；真实 HTTP 已通过 |
| AUCT-21 deposit address missing | 买家 token 创建押金占用，不传 `addressId` | HTTP 200，`result.code=409110` | 确认押金链路必须绑定收货地址。 | 已实现；真实 HTTP 已通过 |
| AUCT-22 deposit address not found | 买家 token 创建押金占用，传不存在的 `addressId` | HTTP 200，`result.code=409111` | 确认不能使用不存在或不属于自己的地址。 | 已实现；真实 HTTP 已通过 |
| AUCT-23 bid without deposit | live 拍品中买家未完成押金占用直接出价 | HTTP 200，`result.code=409109`，`accepted=false` | 确认出价链路会拦截未支付押金的买家。 | 已实现；真实 HTTP 已通过 |
| AUCT-24 deposit idempotent replay | 同一买家、同一 lot、同一押金幂等键重复 `mock-pay` | HTTP 200，`result.code=0`，`paid=true`，返回同一 `depositHold.id` | 确认押金支付重试不会重复创建占用。 | 已实现；真实 HTTP 已通过 |

### Draft Queue Contract Cases

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| DRAFT-01 create empty draft | 商家 token `POST /api/lots/drafts`，只传可选 `room_id` | HTTP 200，`result.code=0`，返回 `lot.id`，`status=LOT_STATUS_DRAFT` | 确认添加拍品页可先创建空草稿容器。 | 已实现；真实 HTTP 已通过 |
| DRAFT-02 queue incomplete draft | 对空草稿 `POST /queue` | HTTP 200，`result.code=400001` | 确认未补全资料不能入队。 | 已实现；真实 HTTP 已通过 |
| DRAFT-03 autosave complete fields | `PATCH /api/lots/{id}/draft`，补齐标题、描述、图片、规则、轮播图、分类、标签、库存 | HTTP 200，`result.code=0`，返回标题和标签等字段 | 确认自动保存能持久化添加拍品页核心字段。 | 已实现；真实 HTTP 已通过 |
| DRAFT-04 queue ready draft | 对补全草稿 `POST /queue` | HTTP 200，`result.code=0`，`status=LOT_STATUS_QUEUED`，`queuePosition>=1` | 确认补全后能进入本场队列。 | 已实现；真实 HTTP 已通过 |
| DRAFT-05 queue repeated | 对同一已入队拍品再次 `POST /queue` | HTTP 200，`result.code=0`，返回相同 `queuePosition` | 确认重复点击“加入队列”幂等，不消耗新队列位。 | 已实现；真实 HTTP 已通过 |
| DRAFT-06 missing title | 草稿 body 删除 `title` 后入队 | HTTP 200，`result.code=400001`，message 包含“标题” | 确认标题必填。 | 已实现；真实 HTTP 已通过 |
| DRAFT-07 missing image | 草稿 body 删除 `image_url` 后入队 | HTTP 200，`result.code=400001`，message 包含“图片” | 确认主图必填。 | 已实现；真实 HTTP 已通过 |
| DRAFT-08 missing rule | 草稿 body 删除 `rule` 后入队 | HTTP 200，`result.code=400001` | 确认竞拍规则必填；不绑定易变化的本地化文案。 | 已实现；真实 HTTP 已通过 |
| DRAFT-09 min increment zero | `rule.min_increment.amount=0` | HTTP 200，`result.code=400001`，message 包含“最低加价” | 确认最低加价必须大于 0。 | 已实现；真实 HTTP 已通过 |
| DRAFT-10 duration too short | `rule.duration_seconds=30` | HTTP 200，`result.code=400001`，message 包含“竞拍时长” | 确认竞拍时长不能少于 60 秒。 | 已实现；真实 HTTP 已通过 |
| DRAFT-11 rule currency mismatch | `start_price=CNY`，`min_increment=USD` | HTTP 200，`result.code=400001`，message 包含“币种” | 确认起拍价和最低加价币种必须一致。 | 已实现；真实 HTTP 已通过 |
| DRAFT-12 invalid cap price | `cap_price.amount` 等于起拍价 | HTTP 200，`result.code=400001` | 确认封顶价必须大于起拍价。 | 已实现；真实 HTTP 已通过 |
| DRAFT-13 invalid image protocol | `image_url=ftp://example.com/bad.jpg` | HTTP 200，`result.code=400001` | 确认图片 URL 只接受 http/https。 | 已实现；真实 HTTP 已通过 |
| DRAFT-14 invalid autosave main image | autosave `image_url=temporary-preview://local-file` | HTTP 200，`result.code=400001` | 确认自动保存阶段也拒绝临时/非法主图 URL。 | 已实现；真实 HTTP 已通过 |
| DRAFT-15 too many gallery images | autosave 传 7 张轮播图 | HTTP 200，`result.code=400001` | 确认轮播图数量上限。 | 已实现；真实 HTTP 已通过 |
| DRAFT-16 negative stock | autosave `stock=-1` | HTTP 200，`result.code=400001` | 确认库存不能为负。 | 已实现；真实 HTTP 已通过 |
| DRAFT-17 deposit currency missing | autosave `deposit_amount.amount=1000`，不传 currency | HTTP 200，`result.code=400001` | 确认保证金金额必须带币种。 | 已实现；真实 HTTP 已通过 |
| DRAFT-18 unauth draft create | 无 token `POST /api/lots/drafts` | HTTP 200，`result.code=401001` | 确认草稿创建需要登录。 | 已实现；真实 HTTP 已通过 |
| DRAFT-19 buyer draft create forbidden | 买家 token `POST /api/lots/drafts` | HTTP 200，`result.code=403001` | 确认买家不能进入商家添加拍品入口。 | 已实现；真实 HTTP 已通过 |
| DRAFT-20 unauth queue | 无 token 对商家草稿 `POST /api/lots/{id}/queue` | HTTP 200，`result.code=401001` | 确认入队操作需要登录。 | 已实现；真实 HTTP 已通过 |
| DRAFT-21 buyer queue forbidden | 买家 token 对商家草稿 `POST /api/lots/{id}/queue` | HTTP 200，`result.code=403001` | 确认买家不能把商家草稿加入直播队列。 | 已实现；真实 HTTP 已通过 |

### Proto Migration Contract Cases

这些用例专门验证“手写业务 JSON route 已迁入 proto 生成 handler”后的对外契约。基础设施 route、WebSocket、multipart upload 不在本组。

| Case ID | 输入 | 预期输出 | 用例目的 | 当前情况 |
| --- | --- | --- | --- | --- |
| PROTO-01 list shop products | `GET /api/shop/products?page=1&pageSize=3`，无 token | HTTP 200，`result.code=0`，返回非空 `products`，商品含 `id` 和 `skus` | 确认商品列表由 `shop.proto` handler 暴露，且种子商品可读。 | 已实现；真实 HTTP 已通过 |
| PROTO-02 get shop product | `GET /api/shop/products/{product_id}`，使用 PROTO-01 返回商品 | HTTP 200，`result.code=0`，`product.id` 等于输入 | 确认商品详情路径变量绑定和响应 payload 正常。 | 已实现；真实 HTTP 已通过 |
| PROTO-03 missing shop product | `GET /api/shop/products/not-found-product` | HTTP 200，`result.code=404001` | 确认缺失商品仍走统一 `result` 外壳。 | 已实现；真实 HTTP 已通过 |
| PROTO-04 shop orders require login | 无 token `GET /api/shop/orders` | HTTP 200，`result.code=401001` | 确认商城订单列表需要买家登录。 | 已实现；真实 HTTP 已通过 |
| PROTO-05 create delivery address | 买家 token `POST /api/shop/addresses`，body 为 proto 契约 `{"address":{...}}` | HTTP 200，`result.code=0`，返回 `address.id` | 确认地址创建已由 `shop.proto` 接管；新契约使用嵌套 `address`。 | 已实现；真实 HTTP 已通过 |
| PROTO-06 list delivery addresses | 买家 token `GET /api/shop/addresses` | HTTP 200，`result.code=0`，`addresses` 非空 | 确认地址列表迁移后仍能读取当前用户地址。 | 已实现；真实 HTTP 已通过 |
| PROTO-07 update delivery address | 买家 token `PUT /api/shop/addresses/{address_id}`，body 为 `{"address":{...}}` | HTTP 200，`result.code=0`，返回更新后的 `receiverName` | 确认地址更新路径变量、body bind 和业务校验正常。 | 已实现；真实 HTTP 已通过 |
| PROTO-08 set default address | 买家 token `POST /api/shop/addresses/{address_id}/default` | HTTP 200，`result.code=0`，返回地址列表 | 确认设默认地址迁移后仍返回稳定列表 payload。 | 已实现；真实 HTTP 已通过 |
| PROTO-09 invalid shop order | 买家 token `POST /api/shop/orders`，`quantity=0` | HTTP 200，`result.code=400001` | 确认生成 handler 后，业务参数错误仍映射稳定业务码。 | 已实现；真实 HTTP 已通过 |
| PROTO-10 create shop order | 买家 token `POST /api/shop/orders`，传 `skuId`、`quantity=1`、`addressId`、幂等键 | HTTP 200，`result.code=0`，返回 `order.id` | 确认商城下单已由 `shop.proto` 接管，并真实写入 MySQL。 | 已实现；真实 HTTP 已通过 |
| PROTO-11 shop mock pay | 买家 token `POST /api/shop/orders/{order_id}/mock-pay`，传支付幂等键 | HTTP 200，`result.code=0`，`paid=true` | 确认商城订单支付 route 迁移后可完成状态流转。 | 已实现；真实 HTTP 已通过 |
| PROTO-12 list unified orders | 买家 token `GET /api/orders/me?source=shop&page=1&pageSize=10` | HTTP 200，`result.code=0`，`orders` 非空 | 确认统一订单列表仍能展示商城订单来源。 | 已实现；真实 HTTP 已通过 |
| PROTO-13 get unified order | 买家 token `GET /api/orders/{order_id}` | HTTP 200，`result.code=0`，`order.id` 等于输入 | 确认统一订单详情路径已迁入 `shop.proto`。 | 已实现；真实 HTTP 已通过 |
| PROTO-14 unified mock pay | 买家 token `POST /api/orders/{order_id}/mock-pay`，传支付幂等键 | HTTP 200，`result.code=0`，`paid=true` | 确认统一支付入口可代理商城订单支付。 | 已实现；真实 HTTP 已通过 |
| PROTO-15 frequent stores | 买家 token `GET /api/orders/me/frequent-stores?limit=5` | HTTP 200，`result.code=0`，返回 `stores` 数组 | 确认常购店铺聚合 route 迁移后仍返回数组外壳。 | 已实现；真实 HTTP 已通过 |
| PROTO-16 delete delivery address | 买家 token `DELETE /api/shop/addresses/{address_id}` | HTTP 200，`result.code=0` | 确认地址删除迁移后可用。 | 已实现；真实 HTTP 已通过 |
| PROTO-17 lot result | 买家 token `GET /api/lots/{lot_id}/result`，lot 为测试创建草稿 | HTTP 200，`result.code=0`，返回 `lot.id` 和 `auctionState` | 确认拍品结果 route 已由 `auction.proto` 生成 handler 接管。 | 已实现；真实 HTTP 已通过 |
| PROTO-18 my auction orders/bids | 买家 token `GET /api/me/orders?page=1&pageSize=5`、`GET /api/me/bids?page=1&pageSize=5` | HTTP 200，`result.code=0`，返回 `orders` / `bids` 数组 | 确认买家竞拍订单和出价记录列表迁移后保留数组契约。 | 已实现；真实 HTTP 已通过 |
| PROTO-19 admin list authz | 无 token `GET /api/admin/rooms`；买家 token `GET /api/admin/lots` | 分别返回 `401001` 和 `403001` | 确认后台列表迁入 proto 后仍经过鉴权/授权。 | 已实现；真实 HTTP 已通过 |
| PROTO-20 admin lists | 商家 token `GET /api/admin/lots`、`/api/admin/orders`、`/api/admin/rooms` | HTTP 200，`result.code=0`，返回对应数组；`admin/lots` 含测试 lot | 确认后台拍品、订单、房间列表均由 proto route 输出。 | 已实现；真实 HTTP 已通过 |
| PROTO-21 public rooms | 无 token `GET /api/rooms` | HTTP 200，`result.code=0`，返回 `rooms` 数组 | 确认公开房间列表迁入 proto 后仍可匿名访问。 | 已实现；真实 HTTP 已通过 |
| PROTO-22 buyer suggestions | 无 token `GET /api/ai/buyer/suggestions?limit=3` | HTTP 200，`result.code=0`，返回非空 `suggestions` | 确认买家推荐搜索词 route 已由 `auction.proto` 接管。 | 已实现；真实 HTTP 已通过 |

## 8 类覆盖索引

现有功能组已按正常路径、鉴权、授权、参数校验、状态机、冲突和幂等、查询边界、错误外壳进行扩展。若某类对该功能组不是自然契约，文档中明确标为不适用，不为了凑数去测试不存在的用户行为。

| 功能组 | 正常路径 | 鉴权 | 授权 | 参数校验 | 状态机 | 冲突和幂等 | 查询边界 | 错误外壳 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `http_contract` | HTTP-01 | HTTP-02 | 不适用，传输层不区分业务角色 | HTTP-03 | 不适用 | 不适用 | 不适用 | HTTP-02、HTTP-03、HTTP-05 |
| `auth_contract` | AUTH-01、AUTH-02、AUTH-04、AUTH-06、AUTH-09、AUTH-11、AUTH-19 | AUTH-23 | 不适用，公共认证入口不做角色授权 | AUTH-12 至 AUTH-18、AUTH-20、AUTH-21 | AUTH-05、AUTH-07、AUTH-10 | AUTH-05、AUTH-07、AUTH-08、AUTH-22 | 不适用，认证入口无列表查询 | AUTH-03、AUTH-12 至 AUTH-23 |
| `rbac_contract` | RBAC-01、RBAC-02、RBAC-11、RBAC-13、RBAC-16、RBAC-17 | RBAC-07 | RBAC-08、RBAC-09 | RBAC-03 至 RBAC-05、RBAC-10、RBAC-12、RBAC-19 | RBAC-14、RBAC-15、RBAC-16、RBAC-17 | 不适用，团队管理当前没有幂等提交契约 | RBAC-18、RBAC-20、RBAC-21、RBAC-22 | RBAC-07 至 RBAC-10、RBAC-12、RBAC-15、RBAC-19 |
| `auction_auth_contract` | AUCT-06、AUCT-14、AUCT-24 | AUCT-01、AUCT-03、AUCT-19 | AUCT-02、AUCT-04、AUCT-05、AUCT-20 | AUCT-09、AUCT-12、AUCT-18、AUCT-21、AUCT-22 | AUCT-10、AUCT-13、AUCT-15、AUCT-16、AUCT-17、AUCT-23 | AUCT-08、AUCT-24 | 不适用，竞拍出价契约不是列表查询 | AUCT-01 至 AUCT-05、AUCT-09 至 AUCT-23 |
| `auction_draft_queue_contract` | DRAFT-01、DRAFT-03、DRAFT-04 | DRAFT-18、DRAFT-20 | DRAFT-19、DRAFT-21 | DRAFT-06 至 DRAFT-17 | DRAFT-02 | DRAFT-05 | 队列位置由 DRAFT-04、DRAFT-05 验证；完整列表查询暂不在本组 | DRAFT-02、DRAFT-06 至 DRAFT-21 |
| `proto_migration_contract` | PROTO-01、PROTO-02、PROTO-05 至 PROTO-08、PROTO-10 至 PROTO-18、PROTO-20 至 PROTO-22 | PROTO-04、PROTO-19 | PROTO-19 | PROTO-09 | PROTO-11、PROTO-14 | PROTO-10、PROTO-11、PROTO-14 使用幂等键链路 | PROTO-01、PROTO-06、PROTO-12、PROTO-15、PROTO-18、PROTO-20、PROTO-21 | PROTO-03、PROTO-04、PROTO-09、PROTO-19 |

## 运行方式

默认不设置服务地址时，e2e 会 `Skip`，保证 `go test ./...` 不依赖本地 MySQL/Redis/HTTP 服务。

```bash
go test ./test/e2e
go test ./...
```

启动后端后运行真实 HTTP e2e：

```bash
LIVE_AUCTION_E2E_BASE_URL=http://127.0.0.1:18080 go test ./test/e2e -count=1 -v
```

可选环境变量：

| 变量 | 说明 |
| --- | --- |
| `LIVE_AUCTION_E2E_BASE_URL` | 后端 HTTP 地址。未设置时跳过 e2e。 |
| `LIVE_AUCTION_E2E_TIMEOUT` | HTTP client 超时，默认 `10s`。 |
| `LIVE_AUCTION_E2E_ROOM_ID` | 可选。草稿链路未设置时会让后端为新商家创建默认房间；如果要直接验证完整 `/api/lots` 成功创建链路，需要提供属于当前测试商家的房间。 |

## 从旧测试迁移来的契约

以下旧的跨层契约测试已改写为顶层 HTTP e2e，并删除原文件：

| 旧文件 | 新覆盖位置 |
| --- | --- |
| `app/auction/service/test/p0_contract_test.go` | `test/e2e/http_contract_test.go` |
| `app/auction/service/test/service_error_test.go` | `test/e2e/http_contract_test.go` |
| `app/auction/service/test/user_test.go` | `test/e2e/auth_contract_test.go`、`test/e2e/rbac_contract_test.go` |
| `app/auction/service/test/auction_auth_test.go` | `test/e2e/auction_auth_contract_test.go` |
| `app/auction/service/test/auction_draft_queue_test.go` | `test/e2e/auction_draft_queue_contract_test.go` |

迁移不是机械搬文件。顶层 `test/e2e` 不能访问 Go `internal` 包，也不能依赖 in-memory test repo，所以这些场景被改写成真实 HTTP 观察：

- 只断言对外稳定字段和业务码。
- 通过公开注册接口创建唯一账号。
- 通过 Bearer token 验证鉴权与权限。
- 通过 HTTP route 验证 proto/generated route 和手写 route 的一致性。

## 仍保留的测试

| 路径 | 保留原因 |
| --- | --- |
| `app/auction/service/test/auction_biz_test.go` | 使用 test store 验证状态机、并发出价、成交、订单、支付、事件隐私等业务闭环。 |
| `app/auction/service/test/ai_assistant_test.go` | 验证买家 AI 查询候选和公开可见性，依赖 service/usecase test store。 |
| `app/auction/service/test/idgen_test.go` | 验证 snowflake ID 生成和并发安全。 |
| `app/auction/service/internal/**/_test.go` | 同包单元/集成测试，覆盖私有转换、worker、repo、hub、storage 等内部行为。 |

后续如果继续“迁移删除”，原则是先在 `test/e2e` 或其他黑盒层补到同等契约，再删旧测试；不能把依赖私有函数的同包测试直接搬到顶层。

## 失败排查

- `LIVE_AUCTION_E2E_BASE_URL` 未设置：测试会 skip，这是预期行为。
- 连接超时：确认 Docker Compose 或本地 server 已启动，`/healthz` 能返回 `{"ok":true}`。
- `result.code` 不符合预期：优先看 `docs/infra/request-response-protocol.md` 和 service 层 `ErrorResult` 映射。
- 注册失败或账号冲突：e2e 会使用纳秒时间戳生成唯一用户名；若仍冲突，检查数据库是否有异常唯一索引或时间源问题。
- 拍品流程失败：不设置 `LIVE_AUCTION_E2E_ROOM_ID` 时应使用默认房间；如果设置了该变量，确认房间属于当前测试商家主账号并处于 active。

## 最近验证

2026-06-16 已启动 Docker Compose 后端并完成真实运行：

```bash
docker compose up --build -d
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
LIVE_AUCTION_E2E_BASE_URL=http://127.0.0.1:18080 go test ./test/e2e -count=1 -v
go test ./...
```

实跑结果：

- `docker compose ps`：gateway、auction-service、核心 worker、MySQL、Redis、Kafka、NATS 均为 healthy。
- `/healthz`、`/readyz`：均返回 `ok=true`。
- `LIVE_AUCTION_E2E_BASE_URL=http://127.0.0.1:18080 go test ./test/e2e -count=1 -v`：PASS。
- `go test ./...`：PASS；未设置 `LIVE_AUCTION_E2E_BASE_URL` 时 e2e 包按设计 skip 真实 HTTP。
- 本轮完成手写业务 HTTP route 到 proto 生成 handler 的迁移后，新增 `proto_migration_contract`，覆盖商品、地址、商城订单、统一订单、后台/公开列表、买家推荐，并再次完成上述真实 HTTP 验证。




## 修复记录

### 本次真实 HTTP 运行发现并修复了两个后端契约点：（2026.6.16）

- 缺失拍品结果页：`/api/lots/{id}/result` 对不存在 lot 现在映射为 `result.code=404001`，不再泄露 `500000`。
- 取消后出价：runtime 快路径在 Redis 拒绝为 `BID_NOT_LIVE` 时，会回读 DB 聚合状态；如果拍品已取消，对外返回 `result.code=409107 LOT_CANCELLED`。

### Proto 迁移实跑发现并修复的后端契约点：（2026.6.16）

- `user_orders.source_payload` 和 `user_order_payments.source_payload` 是 MySQL JSON 列，商城订单创建/支付的手工模型写入不能使用空字符串，已统一写入 `{}`。
- proto JSON 会把 `int64` 编码为字符串，e2e 数字 helper 已兼容字符串数字，避免列表 `total` 等字段误判。

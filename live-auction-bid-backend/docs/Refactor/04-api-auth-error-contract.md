# P2 API Auth Error Contract

说明：`docs/Refactor` 是当前仓库的 Refactor canonical path。旧的 `docs/refactor/**` 镜像已删除，后续 agent 不应再写入小写路径。

## Proto enum contract

`LotStatus` 和 `AuctionEventType` 已正式写入 `api/auction/service/v1/auction.proto`，并已使用 Linux `protoc` 重新生成：

- `api/auction/service/v1/auction.pb.go`
- `api/auction/service/v1/auction_grpc.pb.go`
- `api/auction/service/v1/auction_http.pb.go`

原 `api/auction/service/v1/p1_domain.go` 兼容层已删除。新增/别名 enum：

- `LOT_STATUS_SCHEDULED`
- `LOT_STATUS_EXTENDED`
- `LOT_STATUS_SOLD`
- `LOT_STATUS_FAILED`
- `BID_OUTBID`
- `AUCTION_EXTENDED`
- `AUCTION_CLOSED`
- `ORDER_CREATED`
- `PAYMENT_SUCCESS`

本轮生成命令：

```bash
PATH=/home/ye/go/bin:$PATH make api PROTOC=/tmp/openclaw-tools/protoc/bin/protoc
```

## Result code 合同

| Code | Name | 前端动作 |
| --- | --- | --- |
| `0` | `OK` | 成功 |
| `400001` | `INVALID_ARGUMENT` | 展示参数错误 |
| `401001` | `LOGIN_REQUIRED` | 清理登录态，要求登录 |
| `401002` | `TOKEN_EXPIRED` | 仅此 code 允许 refresh access token 后重试一次 |
| `401003` | `TOKEN_INVALID` | 清理登录态，不 refresh |
| `401004` | `SESSION_EXPIRED` | 清理登录态，不 refresh |
| `401005` | `INVALID_CREDENTIALS` | 展示用户名或密码错误 |
| `403001` | `FORBIDDEN` | 展示无权限 |
| `409001` | `LOT_VERSION_CONFLICT` | 刷新竞拍状态后重试 |
| `409002` | `USERNAME_TAKEN` | 展示用户名已存在 |
| `404001` | `NOT_FOUND` | 展示资源不存在 |
| `500000` | `INTERNAL_ERROR` | 展示通用系统错误，不泄露原始 err |
 
后端 `ErrorResult` 必须稳定输出上述 code。Admin/H5 只在 `TOKEN_EXPIRED` 上 refresh；`LOGIN_REQUIRED`、`TOKEN_INVALID`、`SESSION_EXPIRED` 直接清理登录态。

## P2 查询接口

所有接口都返回统一 `result`。

| Method | Path | 权限 | Query | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/admin/orders` | `ANCHOR/OPERATOR/ADMIN` | `page,pageSize,status,lotId,buyer` | `orders,total,page,pageSize` |
| `GET` | `/api/admin/users` | `ADMIN` | `page,pageSize,role,keyword`；仅后台团队角色，显式查询 `BUYER` fail-fast | `users,total,page,pageSize` |
| `GET` | `/api/admin/lots` | `ANCHOR/OPERATOR/ADMIN` | `page,pageSize,status,keyword,roomId` | `lots,total,page,pageSize` |
| `GET` | `/api/me/orders` | `BUYER` | `page,pageSize,status,lotId` | 当前买家自己的 `orders,total,page,pageSize` |
| `GET` | `/api/me/bids` | `BUYER` | `page,pageSize,lotId` | 当前买家自己的 `bids,total,page,pageSize` |
| `GET` | `/api/lots/{lot_id}/result` | optional auth | 无 | 公开成交结果；仅中标买家和后台角色返回完整 `order` |

## HTTP DTO 保留边界

业务 JSON API 已收敛到 `api/auction/service/v1/*.proto`：

- `auction.proto`：拍品、房间、出价、订单查询、后台拍品/订单/房间、买家 AI 推荐。
- `user.proto`：登录注册、token、当前用户、后台团队用户管理。
- `shop.proto`：商品、地址、保证金、商城订单、统一订单。

`openapi/auction.openapi.json` 是跟随 proto HTTP binding 同步的前端生成合同；Admin 可通过 `npm run generate:api` 生成 `src/shared/api/generated/auction.schema.ts`。不再保留 `domain_http.go` / `order_http.go` / `shop_http.go` 的业务双路由兼容。

仍保留手写的不是普通业务 JSON API：`/healthz`、`/readyz`、`/metrics`、`/api/realtime/ws-ticket`、`/ws/rooms/*`、`/api/uploads/images`。

## 状态和事件合同

LotStatus：`DRAFT`、`READY`、`QUEUED/SCHEDULED`、`LIVE`、`EXTENDED`、`SETTLED/SOLD`、`CANCELLED`、`FAILED`。

OrderStatus：`CREATED`、`PENDING_PAYMENT`、`PAID`、`CANCELLED`、`EXPIRED`、`REFUNDED`。

PaymentStatus：`INIT`、`PROCESSING`、`SUCCESS`、`FAILED`、`CLOSED`。

RealtimeEventType：`BID_ACCEPTED`、`BID_OUTBID`、`AUCTION_EXTENDED`、`AUCTION_CLOSED`、`LOT_SETTLED`、`LOT_CANCELLED`、`ORDER_CREATED`、`PAYMENT_SUCCESS`。

隐私边界：

- WebSocket 不再默认从 URL query 读取 `access_token`。
- room socket 默认是 public；连接后可发送 `AUTH` 消息。
- 只有 `ANCHOR` / `OPERATOR` / `ADMIN` 认证连接接收完整 realtime event。
- public/buyer realtime event 会去掉 `Bid.userId`、`RankingItem.userId`、`Lot.leadingUserId`、`Lot.winnerUserId`、`DuelState.userAId/userBId` 等私密用户标识。
- 公开 WebSocket 的 `ORDER_CREATED` / `PAYMENT_SUCCESS` 不携带真实 `orderId/paymentId`；中标买家和 Admin 通过带鉴权的 `GET /api/lots/{lot_id}/result` 获取完整订单。

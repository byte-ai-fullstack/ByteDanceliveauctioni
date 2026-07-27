# H5 目标蓝图（Target Blueprint）

Status: `L0_COMPLETE`
Baseline measured: 2026-07-26 23:00
L0 approved: 2026-07-26
L0 completed: 2026-07-26
Scope: `live-auction-user-h5`

本文件是 H5 这一轮"对接新后端 + 内部重构"的**唯一施工纲领**。优先级低于
`PROJECT_CHARTER.md`（后端仓）和本仓 `docs/COMPATIBILITY_POLICY.md`，高于任何临时计划。

配套阅读，不在本文重复其内容：

- `docs/PROJECT_ROLE.md` —— H5 的职责边界。
- `docs/KNOWLEDGE_BASE.md` —— 现状事实（路由表、httpClient、控制器状态清单）。
- `docs/FRONTEND_DESIGN_SYSTEM.md` —— token 与新 UI 规则。
- `docs/COMPATIBILITY_POLICY.md` —— `PRE_LAUNCH_NO_LEGACY_COMPAT`，本轮的强制约束。

---

## 1. 目标与非目标

### 1.1 双目标

| # | 目标 | 判定标准 |
|---|---|---|
| A | **对接重构后的后端** | H5 消费的契约与后端 proto 单向同源；后端字段/事件改动能在 CI 被发现，而不是在直播间里表现成"价格不动" |
| B | **内部结构重构与优化** | 契约单源、实时层可推理、控制器与视图解耦、弱网可控；每一步都可独立回滚 |

### 1.2 硬约束（最高优先级）

> **大表现零变化。**

这不是"尽量别改样式"，而是一条可机械验证的验收条件。定义见第 2 节。任何与
"表现不变"冲突的重构，**一律让路**，即使它在架构上更漂亮。

---

## 2. 表现冻结契约（Presentation Freeze Contract）

整份蓝图的地基。没有这一节，"重构不动表现"只是口头承诺。

### 2.1 冻结清单（禁止改动）

1. **CSS 零改动**：`src/app/styles.css`（13,441 行）、`recovered-overlays.css`、
   `live-product-carousel.css`、`src/pages/*-replica.css`、`src/index.css`。
   L0–L3 阶段一律**字节级不变**。
2. **DOM 结构与顺序**：元素层级、标签名、兄弟顺序、条件渲染的分支结果。
3. **className 字符串**：包括拼接产生的类名组合与顺序。
4. **inline style / CSSProperties**：数值与单位不变。
5. **文案**：所有中文可见文本，含出价失败映射文案
   （`useLiveRoomController.ts:29-46` 的 `bidFailureMessage`）。
6. **图标与素材**：`lucide-react` 图标名、图片/视频 URL 生成逻辑
   （`LIVE_AVATAR_POOL`、`livePreviewSourceFor`、`stableAvatarIndex`）。
7. **动效与手势阈值**：`LIVE_ROOM_SWIPE_DISTANCE=52`、`QUICK_SWIPE_MS=280`、
   `WHEEL_SWIPE_DISTANCE=88`、`WHEEL_LOCK_MS=420`、`SETTLE_MS=230`、
   `NOTICE_VISIBLE_MS=3400` 等常量与其语义。
8. **可见时序**：loading/empty/error 三态的出现条件与先后顺序。

### 2.2 允许改动（重构空间）

文件位置与模块边界、hook 拆分与组合、类型定义与来源、reducer/状态形状、
请求发起时机与合并（前提是最终渲染结果一致）、命名、注释、测试。

### 2.3 验收门（每个 PR 必过）

| Gate | 手段 | 判定 |
|---|---|---|
| G1 CSS 冻结 | `npm run check:presentation-freeze` 校验 committed CSS SHA-256 清单 | 所有冻结 CSS 的内容哈希必须匹配 |
| G2 DOM 快照 | jsdom 渲染关键路由/组件 → 归一化 `outerHTML` 与基线对比 | 必须逐字节相同 |
| G3 视觉抽查 | 430×932 视口截图对比（直播间、结果页、订单页、搜索页） | 人工确认无差异 |
| G4 既有门 | `npx tsc -b` + `npm test` + `npm run lint` + `npm run build` | 全绿 |

**G2 需要新增基建**：当前 `node_modules` 里没有 `jsdom` / `@testing-library/*` /
`happy-dom`，5 个测试文件全是纯函数测试（reducer、adapters、errors）。
所以 L0 的第一件事就是把 G2 的能力建起来 —— 否则后面所有"表现不变"都是无据可查。
现有 `design-qa.md` 记录的是 Windows 临时目录里的手工截图，不可复现，不能当门。

---

## 3. 现状基线（2026-07-26 实测）

### 3.1 规模

| 指标 | 值 |
|---|---|
| TS/TSX 文件 | 96 |
| TS/TSX 行数 | 17,928 |
| CSS 行数（`app/styles.css` 单文件） | 13,441 |
| 测试文件 / 用例 | 5 / 18（全部纯函数） |
| 路由懒加载 | 49 处 `lazy()`，已分包 |
| `LiveRoomPage` 产物 chunk | 116 KB |
| `any` / `!` 非空断言 / `eslint-disable` | 0 / 0 / 0 |
| `as` 类型断言 | 62 |

### 3.2 热点文件

| 文件 | 行数 | 问题 |
|---|---|---|
| `features/douyin-shell/components/DouyinHomeShell.tsx` | 1,229 | 巨型视图 |
| `features/live-room/components/LiveRoomView.tsx` | 1,210 | 巨型视图，内部已含 8+ 个未拆出的子组件 |
| `features/live-room/components/LiveProductDetailOverlay.tsx` | 803 | 巨型视图 |
| `pages/SearchPage.tsx` | 690 | 页面内混业务与展示 |
| `features/live-room/hooks/useLiveRoomController.ts` | 578 | **上帝 hook**：20 个 `useState` + 6 个 `useRef`，混合认证表单/面板开关/结果弹窗/队列列表/通知 |
| `features/shop/api/shopApi.ts` | 567 | API 与模型转换混杂 |
| `shared/api/types.ts` | 463 | **手写契约**，44 个导出类型，复制自 proto |
| `shared/api/adapters.ts` | 387 | 见 3.4 |

### 3.3 好消息：`strict` 是免费的

实测把 `strict: true` 加进 `tsconfig.app.json`（96 个源文件全量检查）：

```
错误数：0
```

参照：再叠加 `noUncheckedIndexedAccess` + `exactOptionalPropertyTypes` → 115 个错误。
说明检查器确实生效，而 `strict` 本身已被满足。

**当前 `tsconfig.app.json` / `tsconfig.node.json` 里没有任何 `strict` 字段** ——
也就是说这个项目一直在非严格模式下侥幸写对了。这是本轮最高性价比的一步：
零改码、零表现风险、立刻拿到全量空安全。

### 3.4 契约层的三个结构性缺陷

**缺陷 1：双 key 防御性解析（违反 COMPATIBILITY_POLICY）**

`adapters.ts` 里几乎每个字段都是 `pick(raw, 'camelCase', 'snake_case')`：

```ts
avatarUrl: stringValue(pick(raw, 'avatarUrl', 'avatar_url')),
createdAtUnixMs: pick(raw, 'createdAtUnixMs', 'created_at_unix_ms'),
```

后端 protojson 稳定输出 camelCase。这套 snake 兜底是给"某个已经不存在的旧后端"
留的，属于 `COMPATIBILITY_POLICY.md` 明文禁止的 dual payload parsing，约 200 行纯负债。

**缺陷 2：静默兜底，让契约破裂不可见（本轮最关键的一条）**

```ts
function stringValue(value: unknown, fallback = '') { ... }
function numberValue(value: unknown, fallback = 0) { ... }
function normalizeEnum(value, values, fallback) { ... }   // fallback = *_UNSPECIFIED
currency: money.currency || 'CNY'
```

后端删一个字段、改一个 enum 名、换一个嵌套层级，H5 **不报错**：价格变 `0`、
状态变 `UNSPECIFIED`、昵称变空串，UI 照常渲染。这正是"后端重构了但没人发现前端已经
对不上"的技术根因。`COMPATIBILITY_POLICY.md` 要求的是 fail fast，现状是反的。

**缺陷 3：无契约 codegen**

Admin 有 `generate:api`（`openapi-typescript` 从后端 openapi 生成）+ CI drift gate。
H5 **两者都没有**，`types.ts` 463 行纯手写。后端 proto 今天新增 `Lot.config_version`
（第 36 号字段），H5 无从感知。

### 3.5 实时层缺陷

`features/live-room/model/auctionRoomReducer.ts`（273 行）质量尚可，但：

1. **没有版本单调性检查**。`lot.version` 在类型里存在，reducer 从不比较。
   WS 乱序到达、或快照晚于新事件返回，都会让价格/状态回退。
2. **`mergeEventLot` 做字段级"新值为空就保留旧值"兜底** —— 因为公开事件里的 lot 是
   脱敏裁剪过的。这是用兜底逻辑补契约缺口，而不是让契约表达"公开视图"。
3. **20 个事件类型平铺在一个 `applyPublicEvent` 里**，新增一种事件就多一个 `if`。

### 3.6 REST 端点：已核对，1:1 对齐

把 H5 全部 25 个 `/api/` 路径与后端 52 条 proto HTTP binding 做集合差：

| 结论 | 数量 | 说明 |
|---|---|---|
| H5 使用且有 proto 绑定 | 24 | 全部命中，含 `GET /api/lots`、`DELETE /api/shop/addresses/{id}` |
| H5 使用但**不在** proto 契约内 | 1 | `POST /api/realtime/ws-ticket` —— 在 `server/realtime.go:13` 手工 `HandleFunc` 注册 |

**所以 REST 路由不是风险点。** 风险全部集中在字段级漂移（3.4 缺陷 2）和实时层（3.5）。

`ws-ticket` 游离于 proto 之外意味着它永远进不了 openapi、H5 永远无法为它生成类型 ——
这条要么补进 proto，要么在蓝图里显式登记为"手写契约例外"。

---

## 4. 后端交接面：谁挡着谁

H5 不是孤岛。本轮已知的后端侧问题（详见后端仓当日审计）直接决定 H5 哪些阶段能开工。

| 事项 | 归属 | 状态 | 是否阻塞 H5 |
|---|---|---|---|
| 后端编译失败（`usecase.go:475` `mapRuntimeCommandError` 未定义） | 后端 | 未修 | **阻塞联调**，不阻塞 L0/L1 编码 |
| 出价不再触发 WS 广播（`place_bid.lua` 只 `LPUSH` 不 `XADD`，投影 worker 空转） | 后端 | 未修 | **阻塞 L2 验证** |
| `openapi/auction.openapi.json` 未随 proto 重新生成（仍是 6-16 版本） | 后端 | 未做 | **阻塞 L1**（H5 codegen 的输入） |
| `realtime.proto` 新契约仅被 `event_contract_test.go` 引用，无服务端发送方 | 后端 | 未接线 | **决定 L2 走 A 还是 B** |
| `projector` / `domain-relay` 未进 docker-compose | 后端 | 未做 | 阻塞端到端联调 |
| `Lot.config_version` 新字段 | 后端已加 | 待传导 | L1 顺带接入 |

**H5 可以在后端修好之前先做完 L0，并完成 L1 的大部分改造**（删双 key、改 fail-fast、
搭 codegen 脚本），只在最后一步"跑 generate:api 并提交生成物"时等后端的 openapi。

---

## 5. 目标架构

四层单向依赖，禁止反向引用：

```
┌─ view 层（表现冻结区）────────────────────────────────┐
│  features/*/components, pages/*                      │
│  规则：只读 props 与 controller 返回值；不发请求；     │
│        不碰 localStorage；DOM 与 className 冻结        │
└──────────────────────┬───────────────────────────────┘
                       │ controller hooks
┌──────────────────────▼───────────────────────────────┐
│  features/*/hooks —— 编排层                           │
│  规则：单一职责，一个 hook 一个关注点；                │
│        不做字段解析；不直接 new WebSocket              │
└──────────────────────┬───────────────────────────────┘
                       │ domain model
┌──────────────────────▼───────────────────────────────┐
│  features/*/model, entities/* —— 纯函数域模型          │
│  reducer / 状态机 / 显示状态派生 / 通知映射            │
│  规则：纯函数、可单测、不 import React、不 import 网络 │
└──────────────────────┬───────────────────────────────┘
                       │ contract
┌──────────────────────▼───────────────────────────────┐
│  shared/api（generated + 校验）, shared/realtime       │
│  规则：类型由 openapi 生成，不手写；                   │
│        解析失败 → 抛 ContractError，不静默兜底         │
└──────────────────────────────────────────────────────┘
```

配套三条规则：

- **契约单源**：`shared/api/generated/*` 由脚本生成，人不改。手写只允许存在于
  "视图模型"（generated 类型 → UI 友好形状）和已登记的契约例外（`ws-ticket`）。
- **失败可见**：必需字段缺失 → `ContractError`（带 path + traceId），开发/测试期
  显式暴露；生产期降级为一次性错误上报 + 该区块错误态，**不伪装成正常数据**。
- **实时权威**：所有房间状态变更以 `lot_version` 单调递增为准，旧版本一律丢弃。

---

## 6. 阶段计划

五个阶段，每阶段独立可交付、独立可回滚。**表现风险等级**是最重要的一列。

### L0 — 护栏与基线（表现风险：无）

不改任何业务代码，只把"改坏了能被发现"这件事做出来。

1. 安装 `jsdom`，配置 vitest environment；新增 `src/test/domSnapshot.ts` 归一化工具
   （去掉随机 id、时间戳、`Math.random` 派生值）。
2. 为 6 个关键面建立 DOM 基线快照：直播间（`LIVE`/`SETTLED`/`CANCELLED` 三态）、
   结果页、订单页、搜索页。用固定 mock 数据 + 固定时钟。
3. `tsconfig.app.json` 与 `tsconfig.node.json` 加 `"strict": true`（实测 0 error）。
4. ESLint 升到 `tseslint.configs.recommendedTypeChecked`；新增本地契约负债基线检查：
   `snake_case` 双读与 `shared/api` 默认值兜底的命中数只能减少、不能增加，避免
   L0 被既有负债阻塞，同时保证 L1 可以持续消债。
5. `.github/workflows/ci.yml` 增加 `tsc -b`（当前 CI 只有 lint/test/assets/build）
   与 G1 CSS 冻结检查。G1 使用 committed SHA-256 清单，而不是在干净 checkout 上
   无法识别 PR 内 CSS 改动的 `git diff`。

**L0 已批准施工决策（2026-07-26）**：

- 用独立 `vitest.config.ts` 接入全局 setup，避免把测试职责混入现有 Vite 开发配置。
- 保留已建立的 `LiveRoomView` 七个状态基线；再补结果页、订单页、搜索页。
- 在 API、认证会话、时钟、随机数和 Web Storage 边界提供确定性 mock；生产组件不改。
- DOM 归一化只处理 React 内部不稳定 ID，不能吞掉文案、结构、className 或 inline style 差异。
- 快照缺失、异步未收敛、测试泄漏、CSS 哈希变化或契约负债计数增加都必须使 CI 失败；
  CI 不自动更新任何基线。

**产出**：一条"表现改动会红"的流水线。
**验收**：G1–G4 全绿；故意改一个 className → G2 必须失败。

### L1 — 契约单源与失败可见（表现风险：低，需逐面回归）

前置：后端重新生成 `openapi/auction.openapi.json`。

1. 新增 `npm run generate:api`，与 admin 对齐：
   `openapi-typescript ../live-auction-bid-backend/openapi/auction.openapi.json -o src/shared/api/generated/auction.schema.ts`
2. `types.ts` 463 行 → 拆成 `generated`（来源）+ `viewModel.ts`（UI 形状）+
   `contract.ts`（`RESULT_CODE` 等确实由前端维护的常量）。
3. **删除全部 `pick(camel, snake)` 双读**，只认 camelCase。
4. `adapters.ts` 重写为 `decoders.ts`：必需字段缺失 → `ContractError`；
   可选字段保持可选并在类型上体现，不再用 `''`/`0`/`UNSPECIFIED` 填坑。
5. 接入 `Lot.configVersion`。
6. CI 增加 contract drift gate（`generate:api` 后 `git diff --exit-code`）。
   同时在文档登记 `ws-ticket` 为契约例外。

**风险**：改成 fail-fast 会**立刻暴露真实存在的字段漂移**——这是目的，不是回归。
每个暴露点要么后端补字段，要么前端把它正式声明为可选。
**验收**：G1–G4 + 每个业务面手工过一遍（登录、进房、出价、结算、下单、支付、地址）。

### L2 — 实时层重构（表现风险：中）

**已决策（2026-07-26）：先做 L2-A，L2-B 作为后端就绪后的续期。**
即本轮不要求后端立刻接线 `realtime.proto`；H5 先在现有 `AuctionEvent` 契约上加固。

**L2-A（后端暂不启用 realtime.proto）—— 可立即独立交付**

1. reducer 引入 `lot_version` 守卫：`incoming.version < current.version` → 丢弃。
2. 事件分发从 `if` 链改为 `Record<AuctionEventType, Handler>` 表，缺失即编译报错。
3. 拆掉 `mergeEventLot` 的字段级兜底，改为显式的"公开视图 + 已知缺省字段"模型。
4. `roomSocket` 补：连接状态与重连次数上报、快照恢复退避、`AbortSignal`。

**L2-B（后端启用 realtime.proto）—— 需后端先发消息**

在 A 的基础上，把房间状态模型换成后端新契约的三件套：

- `RoomSnapshotPublicV1` —— 全量脱敏公开快照（含 `lotVersion`、`topRanking`、`settlement`）
- `PersonalDeltaV1` —— 同版本号上叠加个人私有态（`yourRank`、`youAreLeading`、
  `yourOrderId`、`orderVisibility`、`tombstone`）
- `RoomHeartbeatV1` —— `authoritativeLotVersion` 与本地版本不一致即主动补快照

收益是把"公开态/私有态混在一个事件里再靠 viewer 裁剪"换成两条正交流，
`ownOrderForLot` 这类隐私兜底可以退役；`orderVisibility` 还能替掉现在对
`GET /api/lots/{id}/result` 的轮询式同步。

**表现约束**：两条路径都不允许改变通知文案与出现时机
（`notices.ts` + `NOTICE_VISIBLE_MS`）。
**验收**：G1–G4 + 弱网场景脚本（断线 30s 重连、乱序事件注入、心跳超时）+
100 并发出价下价格/排名与后端一致。

### L3 — 状态与组件架构（表现风险：中高，但纯机械可控）

1. **拆 `useLiveRoomController`（578 行 / 20 个 useState）** 为职责单一的 hook：
   - `useBuyerAuthForm` —— 认证表单 6 个 state
   - `useAuctionPanels` —— 面板/抽屉开关与 tab
   - `useSettlementResult` —— 结果弹窗、已读/已关闭集合、私有结果同步
   - `useBidSubmission` —— 出价、押金、幂等键、失败文案
   - `useLiveRoomController` 退化为组合器，对外返回的 `LiveRoomController`
     **形状与字段名保持不变**（视图零改动）。
2. **拆巨型视图**：`LiveRoomView.tsx` 内部已有 `LiveRoomChrome`、
   `LiveBidLeaderboard`、`LiveRoomEffectsLayer`、`LiveProductFloatCard`、
   `ComposerIcon`、`AvatarMedia` 等边界清晰的子组件 —— **按原样搬到独立文件**，
   JSX 一字不改。同法处理 `LiveProductDetailOverlay`、`DouyinHomeShell`、`SearchPage`。
3. 视图内的展示常量（`SHARE_OPTIONS`、`GIFT_OPTIONS`、`MORE_ACTIONS`、
   `LIVE_AVATAR_POOL`）移到 `features/live-room/config/`，值不变。
4. `shopApi.ts`（567 行）按资源拆分，API 调用与解码分离。

**铁律**：这一阶段的每个 commit 都必须 G2 逐字节通过。**一个 commit 只做一种搬移**，
禁止"搬文件顺手改逻辑"。
**验收**：G1–G4 + 每个被拆文件的 DOM 快照零 diff。

### L4 — 弱网与性能（表现风险：低）

1. `httpClient` 现在**没有超时、没有 `AbortSignal`**（`httpClient.ts:159-171`）——
   移动端弱网必需项，补齐并统一取消语义。
2. 路由切换/组件卸载时取消在途请求，消除竞态与卸载后 setState。
3. 同一 key 的并发请求去重（房间快照、订单列表）。
4. `LiveRoomPage` chunk 116 KB → 把详情浮层、礼物特效等非首屏路径二次分包。
5. 补 `useLivePlayer` 与视频源的失败退避（当前失败即静默）。

**验收**：G1–G4 + 弱网剧本（3G 限速、随机丢包）下无白屏无卡死 + chunk 体积不回升。

---

## 7. 明确不做

| 不做 | 原因 |
|---|---|
| 动 `styles.css` / 任何 CSS | 与硬约束直接冲突；13,441 行的级联顺序不可安全重排 |
| 引入 Tailwind / CSS-in-JS / UI 组件库 | 必然改变 DOM 与 className |
| 换掉手写 router（`app/router.tsx`，49 处 lazy 已工作） | 高风险零收益 |
| 引入 Redux / zustand / TanStack Query | 现有 reducer + hook 足够；替换会重写全部数据流 |
| 抽 h5/admin 共享 runtime 包 | 两者是**独立 git 仓库**，共享包需要新的发布链路。共享只走"同一份 openapi 各自生成"，这已覆盖 ~600 行重复基建（httpClient 190/220、roomSocket 166/206、types 162/463）中的契约部分 |
| 重做抖音壳复刻页（Music/Message/Profile 等） | 纯表现复刻，业务价值为零，改动风险极高 |
| 在本轮修后端 | 后端问题已单独登记，归后端仓 |

---

## 8. 验证命令

```bash
# 每个 PR
npm run lint
npx tsc -b
npm test                                   # 含 L0 起的 DOM 快照
npm run build
npm run check:presentation-freeze          # committed CSS SHA-256 清单
npm run check:contract-guardrails           # 既有契约负债只减不增

# L1 起
npm run generate:api && git diff --exit-code -- src/shared/api/generated/

# 联调（需后端可用）
npm run dev                                # 走 VITE_API_BASE_URL / VITE_WS_BASE
```

---

## 9. 交付物

每个阶段结束产出一份 `docs/Refactor/reports/H5_L{n}_*.md`，沿用现有报告格式
（范围 / 动作 / 验证命令与结果 / 遗留问题 / 是否可进入下一阶段），并同步更新
`docs/KNOWLEDGE_BASE.md` 的对应事实章节。

---

## 10. 后续阶段待决策（进入相应阶段前确认）

1. ~~**L2 走 A 还是 B？**~~ **已决（2026-07-26）：先做 A，B 作为后端就绪后的续期。**
   因此第 4 节里"`realtime.proto` 未接线"对 H5 **不再是阻塞项**，降级为后续期依赖。
2. **fail-fast 的生产行为**：契约错误在生产是"整块错误态"还是"静默上报 + 保留上次
   有效值"？建议开发/测试严格抛错，生产按区块降级并上报。
3. **`ws-ticket` 是否补进 proto**？补则契约 100% 覆盖；不补则永久登记为例外。
4. **是否顺带开 `noUncheckedIndexedAccess` + `exactOptionalPropertyTypes`**？
   实测 115 个错误，属于独立的一轮硬化工作，建议放到 L4 之后单独评估。

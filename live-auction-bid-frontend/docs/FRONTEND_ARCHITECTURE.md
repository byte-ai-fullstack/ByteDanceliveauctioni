# 商家后台前端架构

> 更新日期：2026-07-26
>
> 当前范围：`/login`、`/admin/**` 与 `/host/**` 主播团队工作台。
>
> `src/pages/home/HomePage.tsx` 及买家 H5 由独立任务维护，不属于本文和本轮后台重构范围。

## 1. 架构目标

后台以服务端 REST 契约和 WebSocket 版本链为唯一事实来源，同时保持既有 UI 表现不变：

- REST 列表与详情由 TanStack Query 负责缓存、去重、取消和错误状态；
- 实时价格、排名和拍品状态由 `RoomChannel` 按服务端 `lotVersion` 合并；
- 后台路由、标题、导航和页面渲染从一张路由表派生；
- OpenAPI 与实时协议类型均从后端契约生成，CI 阻止漂移；
- 四个样式文件、11 条后台路由 DOM 和 22 张基准截图均为冻结资产。

## 2. 目录与职责

```text
src/
├── app/                         # 应用入口、QueryClient 装配、全局样式
├── entities/                    # 拍品、订单、用户的领域状态与显示语义
├── features/
│   ├── auth/                    # 登录、登出、注册、重置密码 API
│   ├── auction/                 # 拍品与房间 REST API
│   ├── auction-create/          # 添加拍品页面与纯表单模型
│   ├── auction-manage/          # Dashboard、队列、历史及拆分后的分析/展示模块
│   ├── order/                   # 订单 REST API 与契约
│   ├── order-manage/            # 成交处理页面与 Query 编排
│   ├── realtime-console/        # 中控、出价明细、实时诊断及 Query 编排
│   └── team-accounts/           # 团队成员页面与 Query 编排
├── pages/
│   ├── host-console/            # 后台 Shell、唯一路由表、DOM 冻结快照
│   ├── login/                   # 后台认证入口
│   └── home/                    # 其他任务所有，本轮后台不修改
├── shared/
│   ├── api/                     # httpClient、Query 配置、生成 OpenAPI、normalizer
│   ├── auth/                    # 会话存储、续期、权限保护
│   ├── realtime/                # 生成 WS 契约、RoomChannel、RoomSocket
│   ├── router/                  # 轻量 History API 导航
│   ├── config/                  # 环境与固定主播空间配置
│   └── lib/                     # 时间、金额、日志工具
└── test/                        # DOM 测试支持
```

非生成的后台 TypeScript 文件均不超过 400 行；当前最大的后台页面文件为 331 行。生成的 OpenAPI 文件和由其他任务维护的 `HomePage.tsx` 不计入后台拆分指标。

## 3. 应用与路由

`src/app/App.tsx` 只负责三个顶层入口：Home、Login、受保护的后台。`AuthSessionProvider` 维护令牌续期和过期跳转，`ProtectedRoute` 校验后台权限。

后台的 11 条规范路由集中在 `pages/host-console/routes.tsx`：

| 路由 | 页面 |
| --- | --- |
| `/admin` | 今日工作台 |
| `/admin/auctions/current/control` | 直播间中控台 |
| `/admin/auctions/create` | 添加拍品 |
| `/admin/auctions` | 本场拍品队列 |
| `/admin/auctions/history` | 拍品历史 |
| `/admin/orders` | 成交处理 |
| `/admin/bids` | 出价明细 |
| `/admin/merchants` | 团队成员 |
| `/admin/realtime` | 直播健康 |
| `/admin/settings` | 工作台设置 |
| `/admin/alerts` | 异常告警 |

每条路由同时声明 `path`、`aliases`、`title`、`nav` 和 `render`；`/host/**` 是兼容别名。侧栏高亮、页面标题和渲染目标不得再维护额外匹配表。

站内导航统一使用 `AppLink` 或 `navigateApp`。它们通过 History API 更新 URL，并由 `useSyncExternalStore` 通知 React；浏览器前进、后退仍由 `popstate` 驱动。后台站内代码禁止给 `location.href` 赋值，ESLint 常驻阻断该写法。

## 4. REST 数据层

`QueryClientProvider` 在应用入口统一装配。默认策略：

- `staleTime: 15s`；
- `gcTime: 5min`；
- 查询和 mutation 不自动重试；
- 窗口重新聚焦不自动刷新。

`shared/api/queryKeys.ts` 是后台 query key 的唯一注册处。页面的 `model/use*Page.ts` 负责查询、筛选、分页和显式刷新，展示组件不直接发请求。

所有 query function 都接收 TanStack Query 提供的 `AbortSignal`，再传到 API service 和 `httpClient` 的底层 `fetch`。快速切换筛选时，过期请求会真正中止，不再使用只能阻止 `setState` 的 `alive` 标志。

HTTP 响应仍经过两层适配：

1. `result.ts` 统一处理 reply 中的业务 `result`；
2. `normalizers.ts` 只做 camelCase 输入的运行时校验、默认值和领域形状收敛。

后台不保留 snake_case 兼容分支，也不允许 mock 与真实数据并存。尚无后端契约的设置和告警能力保持明确的静态占位，不伪造业务数据。

## 5. 实时版本链

### 5.1 数据流

```text
admin ws-ticket
  → /ws/rooms/:roomId?client_app=admin-web&scope=admin
  → protojson camelCase RealtimeEnvelopeV1
  → normalizeRealtimeEnvelope
  → RoomChannel
  → onSnapshot 更新当前页面视图
```

连接建立或重连后先调用 REST snapshot 建立基线。之后 `RoomChannel` 执行以下规则：

| 输入 | 行为 |
| --- | --- |
| 其他房间 | 忽略 |
| 更低版本 | 作为 stale 忽略 |
| 相同版本 | 作为 duplicate 忽略 |
| `current + 1` | 合并运营快照并提交视图 |
| 跳版本 | 单飞 REST snapshot 回源 |
| 心跳拍品、状态或权威版本不一致 | 单飞 REST snapshot 回源 |
| 断线重连 | full jitter 退避，成功后回源 |

旧的 `AUCTION_REFRESH_EVENTS`、`HTTP_REFRESH_EVENTS`、`ORDER_REFRESH_EVENTS` 和客户端本地 `seq` 已删除。正常实时版本不会触发 lots、orders 或 snapshot 的 HTTP 全量重拉；REST 回源只服务于 gap、心跳不一致和重连恢复。

Dashboard 的服务器时间分析随权威快照更新；1 秒倒计时由共享的叶子时钟驱动，不再让页面级状态每秒重算全部经营分析。

### 5.2 隐私边界

后台真实买家身份只能来自服务端鉴权后下发的 `adminSnapshot`。公共 `publicSnapshot` normalizer 只输出 `maskedNickname`、`maskedAvatarUrl` 等脱敏字段，即使输入意外混入 `userId` 或 `nickname` 也会丢弃。

这条边界由单元测试和 CI 固定；前端不会依据“当前是后台页面”去解开公共消息。

## 6. 契约生成

两类生成物均为实际运行时类型来源：

| 生成物 | 后端来源 | 消费方 |
| --- | --- | --- |
| `shared/api/generated/auction.schema.ts` | `openapi/auction.openapi.json` | `shared/api/types.ts` |
| `shared/realtime/generated/realtime.contract.ts` | `realtime.proto` 与相关枚举 | `realtimeEnvelope.ts` |

生成命令：

```bash
npm run generate:openapi
npm run generate:realtime
npm run generate:api
```

CI 重新生成两份契约并执行 `git diff --exit-code`。生成物漂移必须通过更新契约和适配代码显式解决，不能手改生成文件或增加旧协议兼容分支。

## 7. Dashboard 与页面拆分

`AdminDashboardPage.tsx` 只保留页面编排，目前 138 行：

- `model/useAdminDashboardPage.ts`：Query 与 snapshot 恢复；
- `model/dashboardAnalytics.ts`：纯经营分析；
- `ui/DashboardMetrics.tsx`：指标卡；
- `ui/DashboardCharts.tsx`：漏斗、趋势、排行；
- `ui/DashboardPanels.tsx`：实时焦点、行动清单、风险和明细；
- `ui/useSecondClock.ts`：倒计时叶子共享时钟；
- `ui/useViewAnimations.ts`：既有可见动画触发。

添加拍品页的表单类型、常量、校验和 request 构造收敛到 `auction-create/model/auctionCreateForm.ts`，页面保留原 JSX 和交互编排。API normalizer 的枚举与标量工具则拆到 `normalizerPrimitives.ts`。

## 8. 表现冻结

本轮后台重构禁止修改既有像素、DOM 层级、`className` 和可见文案。门禁为：

| 门禁 | 当前范围 |
| --- | --- |
| 样式字节锁 | 4 个 CSS，809,319 B，SHA-256 逐文件校验 |
| DOM 快照 | 11 条后台路由 |
| Playwright 视觉回归 | 11 路由 × 2 视口，共 22 张，`maxDiffPixels: 0` |

确需变更表现时，应单独立项并显式更新锁文件和基准图，不能夹带在架构重构中。

## 9. CI 与性能预算

CI 顺序覆盖：生产构建、样式锁、单元与 DOM 测试、阻断式后台 lint、Knip、22 张视觉回归、后台性能基线、生成契约漂移。

当前固定基线结果：

- 后台客户端导航：0 次文档重载，0 次重复 rooms 请求；
- 5 个实时页面各处理 100 条运营快照：0 次业务 HTTP 回源；
- 快速订单筛选：3 个请求中止 2 个过期请求，最终 0 在途；
- API AbortSignal 覆盖：30/30，100%；
- 后台 JS：442,173 B / 447,000 B 预算；统计时排除由其他任务维护的 HomePage chunk；
- Dashboard 页面级 1 秒时钟：不存在。

本地总验收：

```bash
npm run build
npm run check:visual-lock
npm run test
npm run lint:admin
npm run knip
npm run test:visual
npm run baseline:admin
npm run generate:api
```

## 10. 变更规则

- 新增后台路由只改 `routes.tsx` 一处；
- 新增服务端查询必须注册 query key，并贯穿 `AbortSignal`；
- 实时消息不得用作“每条事件刷新 HTTP”的广播器；
- 后端协议变化先重新生成契约，再修改运行时校验与页面；
- 公共实时消息不得读取真实身份字段；
- 不修改 `pages/home/HomePage.tsx` 或买家 H5；
- 不在本仓库任务中改后端实现。

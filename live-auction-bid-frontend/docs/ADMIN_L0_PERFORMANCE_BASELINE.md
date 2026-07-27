# 后台性能基线与重构结果

> 采样日期：2026-07-26
>
> 环境：Ubuntu 24.04 / Node.js 24.14.1 / Playwright 1.60.0 / Chromium 148 / 1440×900
>
> 范围：主播团队工作台后台；HomePage/H5 chunk 由其他任务维护，不计入后台 JS 预算。

## 复现

```bash
npm run baseline:admin
```

`prebaseline:admin` 会先执行生产构建。用例使用固定 REST、WebSocket 和时钟 fixture，不依赖正在运行的后端或 H5。每项指标以 `ADMIN_BASELINE {JSON}` 输出机器可读结果。

## L0 与当前结果

| 指标 | L0 | 当前结果 | 验收 |
| --- | ---: | ---: | ---: |
| 后台内链导航新增文档请求 | 1 | 0 | 0 |
| 导航后的重复 rooms 请求 | 1 | 0 | 0 |
| Dashboard 100 条运营快照触发的业务读取 | 未覆盖该页面 | 0 | ≤2 QPS |
| 拍品队列 100 条运营快照触发的业务读取 | 风险代码存在 | 0 | ≤2 QPS |
| 直播中控 100 条运营快照触发的业务读取 | 风险代码存在 | 0 | ≤2 QPS |
| 出价明细 100 条运营快照触发的业务读取 | 风险代码存在 | 0 | ≤2 QPS |
| 实时诊断 100 条运营快照触发的业务读取 | 0 | 0 | ≤2 QPS |
| 快速筛选请求中止 | 0% | 2/2 过期请求中止，0 在途 | 100% |
| API `AbortSignal` 覆盖 | 0/33 | 30/30 | 100% |
| Dashboard 页面级 1 秒时钟 | 存在 | 不存在 | 不存在 |
| Dashboard 页面文件 | 1,073 行 | 138 行 | ≤400 行 |
| 后台 JS | 未单独统计 | 442,173 B | ≤447,000 B |

## 实时负载口径

基线会在约 1.4–1.7 秒内向每个页面发送 100 条连续的 `adminSnapshot`，随后统计以下业务读取：

- `/api/admin/lots`；
- `/api/admin/orders`；
- `/api/rooms/:roomId/snapshot`。

Dashboard、拍品队列、直播中控、出价明细、实时诊断五条路径当前均为 0 次。正常连续版本直接合并为视图状态；只有版本 gap、心跳不一致或重连会调用 snapshot 恢复。

## Dashboard 时钟口径

React DevTools 根提交采样仍约为 4 次 / 3.2 秒，因为倒计时叶子需要每秒提交显示更新。变化在更新范围：

- L0：页面级 `nowMs` 让完整 Dashboard 和经营分析每秒重算；
- 当前：`AdminDashboardPage.tsx` 不再持有或订阅 1 秒时钟；
- 当前：共享时钟只被实时焦点倒计时和订单倒计时叶子消费；
- 经营分析只在 lots、orders、权威 snapshot 或时间范围变化时重算。

因此根提交次数不作为“整页重算”的代理指标；测试同时静态断言页面文件不包含 `setInterval` 或 `useSecondClock`。

## JS 预算口径

当前生产构建：

```text
全部 JS：466,960 B / 140,275 B gzip
HomePage chunk：24,787 B / 8,070 B gzip
后台口径：442,173 B
后台预算：447,000 B
```

后台口径排除 `HomePage-*` chunk，是因为 Home/H5 由另一个任务独立优化；公共 React、入口、登录、图标和 HostConsole chunk 仍全部计入后台。

## 当前机器可读样例

```text
ADMIN_BASELINE {"metric":"navigation","documentNavigations":0,"repeatedRoomRequests":0}
ADMIN_BASELINE {"metric":"realtime-http-qps","route":"/admin","inputEvents":100,"businessReads":0,"requestsPerSecond":0}
ADMIN_BASELINE {"metric":"realtime-http-qps","route":"/admin/auctions","inputEvents":100,"businessReads":0,"requestsPerSecond":0}
ADMIN_BASELINE {"metric":"realtime-http-qps","route":"/admin/auctions/current/control","inputEvents":100,"businessReads":0,"requestsPerSecond":0}
ADMIN_BASELINE {"metric":"realtime-http-qps","route":"/admin/bids","inputEvents":100,"businessReads":0,"requestsPerSecond":0}
ADMIN_BASELINE {"metric":"realtime-http-qps","route":"/admin/realtime","inputEvents":100,"businessReads":0,"requestsPerSecond":0}
ADMIN_BASELINE {"metric":"rapid-filter-abort","started":3,"aborted":2,"inFlight":0}
ADMIN_BASELINE {"metric":"dashboard-root-commits","durationSeconds":3.2,"rootCommits":4,"commitsPerSecond":1.25}
ADMIN_BASELINE {"metric":"static-and-build","apiCallSites":30,"abortableCallSites":30,"abortCoveragePercent":100,"dashboardLines":138,"pageLevelOneSecondTimer":false,"jsBytes":466960,"adminJsBytes":442173,"adminJsBudgetBytes":447000}
```

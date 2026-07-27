# 埋点与事件管线

## 通用目标

把业务事件、用户画像、在线状态上报抽象成统一 analytics client，并保证上报失败有 backup 或 retry。事件字段和用户属性必须分清楚，别把“发生过什么”和“用户现在是什么状态”搅成一锅东北乱炖。

## 适用场景

适用于接入 TapDB、AppsFlyer、ThinkingData、Firebase Analytics、内部 BI 的业务系统。

## 通用抽象

- `AnalyticsClient`：定义 `TrackEvent`、`TrackBatchEvent`、`TrackOnline`、`UserSet`、`UserSetOnce`、`UserAdd`。
- `EventName`：统一事件名常量，按领域拆分。
- `CommonEventParams`：每个事件都带的用户、版本、平台、服务器、等级等公共字段。
- `AsyncTrackRunner`：异步上报包装，带 recover、timeout、trace span。
- `BackupWriter`：上报前或失败时写本地文件，支持 logrotate reopen。
- `RetryQueue`：用 Redis/list/stream 保存待重试 payload。

## 核心流程

1. 服务启动读取 analytics 配置，按 enable 开关初始化客户端。
2. 业务逻辑在状态变更成功后构造事件参数。
3. 通过 `AsyncTrackRunner` 异步上报，避免阻塞主链路。
4. 异步函数使用新的 timeout context，不复用已结束的请求 context。
5. 上报前后按配置写 backup log，便于重放或审计。
6. 网络失败或非成功响应进入 retry queue。
7. 用户画像用 `UserSet/UserSetOnce/UserAdd`，业务行为用 `TrackEvent/TrackBatchEvent`。

## 可变点

- backup 可以是本地文件、对象存储或 Kafka；当前竞拍事实使用 Kafka 作为耐久事件日志。
- retry 可由当前进程扫描，也可交给独立 worker。
- 是否同步等待埋点要按业务价值判断；关键审计事件可同步，普通行为事件异步。
- 多埋点平台可共用事件模型，但平台字段映射要单独隔离。

## Odin 参考实现

- `/home/dministrator/odin/pkg/analytics/analytics.go`：`AnalyticsClient`
- `/home/dministrator/odin/pkg/analytics/tap_db.go`：TapDB client、backup log、retry key
- `/home/dministrator/odin/pkg/analytics/af.go`：AppsFlyer client
- `/home/dministrator/odin/app/loli/service/internal/biz/analytics_tap.go`：事件名常量、`NewTapDBClient`
- `/home/dministrator/odin/app/loli/service/internal/biz/biz.go`：`GoTrack`

## 落地模板

```go
func GoTrack(ctx context.Context, eventName string, f func(context.Context) error) {
    if ctx.Value(DisableTrackKey{}) != nil {
        return
    }
    go func() {
        defer recoverAndLog("track")
        bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := f(bgCtx); err != nil {
            log.Errorf("track %s failed: %+v", eventName, err)
        }
    }()
}
```


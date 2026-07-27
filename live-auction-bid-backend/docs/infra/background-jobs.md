# 后台任务、定时器与扫描循环

## 通用目标

把周期任务、延迟任务、异步任务统一管理，保证有 recover、有 timeout、有退出信号或清理机制。后台任务不是“开个 goroutine 就完事”，那叫把问题扔进黑屋里。

## 适用场景

适用于心跳扫描、在线状态下线、配置变更后刷新模板、埋点重试、周期发奖、缓存清理、定时活动切换。

## 通用抽象

- `GoWithRecover`：所有无返回 goroutine 的基础包装。
- `AsyncTaskRunner`：创建 timeout context、记录 panic、打日志。
- `TimerWheel`：大量延迟任务的轻量定时器。
- `CronTask`：按固定时间或固定间隔执行的任务。
- `ScanLoop`：周期扫描 DB/Redis 并触发补偿。
- `StopSignal`：服务退出时通知后台任务停止。

## 核心流程

1. usecase 初始化时注册必要后台任务。
2. 每个 goroutine 都包 recover，panic 只影响当前任务。
3. 异步任务使用 `context.WithTimeout(context.Background(), limit)`，不复用请求 context。
4. 周期任务要有可关闭的 done channel 或 lifecycle hook。
5. 扫描任务要记录进度，避免重启后重复扫全量或漏扫。
6. 任务失败要按任务重要性选择重试、跳过、报警。

## 可变点

- 简单任务可用 `time.Ticker`，复杂调度可用 cron 库或任务平台。
- 延迟任务量大时用 timer wheel，小规模直接 `time.AfterFunc` 即可。
- 分布式任务要加 Redis lock、DB lease 或调度平台保证单实例执行。
- 是否启动后台任务应由配置开关控制，便于测试和灰度。

## 落地模板

```go
func GoWithRecover(name string, f func()) {
    go func() {
        defer func() {
            if r := recover(); r != nil {
                log.Errorf("%s panic: %v stack=%s", name, r, debug.Stack())
            }
        }()
        f()
    }()
}

func StartScanLoop(done <-chan struct{}, interval time.Duration, scan func(context.Context) error) {
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-done:
            return
        case <-ticker.C:
            ctx, cancel := context.WithTimeout(context.Background(), interval/2)
            _ = scan(ctx)
            cancel()
        }
    }
}
```

## 禁止照抄点

- 不要所有任务都无脑放 usecase 构造函数里启动；要确认生命周期和开关。
- 不要异步任务直接持有请求 context。
- 不要 panic 后静默吞掉，至少记录任务名和堆栈。
- 不要分布式多实例同时跑同一个全局扫描任务，除非任务天然幂等。
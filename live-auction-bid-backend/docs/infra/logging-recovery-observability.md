# 日志、恢复与可观测性

## 通用目标

每个请求都能定位：谁调用、调用哪个接口、耗时多少、参数摘要是什么、错误码和堆栈是什么。panic 必须被 recovery 收口，指标和 trace 要能串起跨服务链路。

## 适用场景

适用于所有线上服务，尤其是请求体加密、大 payload、合批接口、异步埋点、RPC 链路多的系统。

## 通用抽象

- `StructuredLogger`：统一字段输出，包含 service、version、build_time、ts、caller。
- `RequestLoggingMiddleware`：记录 latency、component、operation、args、code、reason、stack。
- `PayloadExtractor`：只输出请求摘要，支持加密 payload 解码、字段截断和大包省略。
- `RecoveryMiddleware`：捕获 panic，打出 operation、args、stack，返回统一错误。
- `MetricsMiddleware`：按 method/server/code/label 统计请求数。
- `TracingMiddleware`：注入和传播 trace context。

## 核心流程

1. 服务启动时根据配置选择 stdout、fluent、file 等 logger。
2. 请求进入后先经过 recovery，保证后续 panic 不打穿进程。
3. metrics 记录请求计数，tracing 创建 span 并传播上下文。
4. logging 在 handler 返回后记录耗时、operation、参数摘要和错误。
5. 对加密或大 payload，只打印解码后的有限字段或统一省略提示。
6. panic 时 recovery 复用参数提取逻辑，避免只有堆栈没有请求上下文。

## 可变点

- 日志出口可用 stdout、fluentd、filebeat、云日志。
- payload 摘要可按协议实现，重点是脱敏、截断、不打印原始大 bytes。
- 业务拒绝类错误不一定打 error，可按项目日志等级规范区分。
- trace 可上报 Jaeger、OTLP collector 或云 APM。

## 落地模板

```go
logFields := []any{
    "latency", time.Since(start).Seconds(),
    "kind", "server",
    "component", component,
    "operation", operation,
    "args", ExtractRequestSummary(req),
}
if err != nil {
    logFields = append(logFields, "code", code, "reason", reason, "stack", fmt.Sprintf("%+v", err))
}
logger.Log(level, logFields...)
```


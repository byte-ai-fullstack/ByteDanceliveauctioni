# HTTP/gRPC 传输层

## 通用目标

用同一套业务 handler 同时承载 HTTP 和 gRPC，HTTP 面向客户端或公网入口，gRPC 面向服务间调用。传输层只管协议、中间件和注册，不写业务规则。

## 适用场景

适用于需要客户端 HTTP 接入、内部服务 gRPC 调用、自动生成 OpenAPI、按 proto 维护接口契约的服务。

## 通用抽象

- `HTTPServerFactory`：读取 `Server.HTTP` 配置，注册 HTTP middleware 和路由。
- `GRPCServerFactory`：读取 `Server.GRPC` 配置，注册 gRPC middleware 和 service。
- `MetricsServerFactory`：单独暴露 `/metrics`，避免业务 server 路由和监控路由互相污染。
- `ServiceHandler`：实现生成代码要求的接口，内部转调 usecase。
- `ManualJsonRoute`：兼容 webhook、旧客户端或不适合 protojson 的 JSON 路由。

## 核心流程

1. 从配置读取 network、addr、timeout。
2. 构造中间件链：recovery、metrics、tracing、logging。
3. 创建 HTTP server 并注册 proto 生成的 HTTP handler。
4. 创建 gRPC server 并注册 proto 生成的 service server。
5. 对 webhook、启动配置等特殊入口使用手写 JSON handler。
6. 独立创建 metrics server，只挂 `/metrics`。

## 可变点

- 中间件顺序可按框架调整，但 recovery 应尽量靠前，logging 应能拿到最终错误。
- 对公网 HTTP 可增加 CORS、限流、鉴权；内部 gRPC 可增加熔断和重试。
- 大消息服务可配置 gRPC max recv/send size。
- 手写 JSON 路由只用于确实不能用生成代码表达的入口。

## 落地模板

```go
func NewHTTPServer(c *ServerConfig, logger Logger, tracer Tracer, h *GameService) *http.Server {
    srv := http.NewServer(
        http.Middleware(
            Recovery(),
            Metrics(),
            Tracing(tracer),
            RequestLogging(logger),
        ),
        http.Address(c.HTTP.Addr),
        http.Timeout(c.HTTP.Timeout),
    )
    RegisterGameServiceHTTPServer(srv, h)
    RegisterManualJsonHTTPServer(srv, h)
    return srv
}
```

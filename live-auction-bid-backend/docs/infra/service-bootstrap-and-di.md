# 服务启动与依赖注入

## 通用目标

让每个服务用同一套启动生命周期：加载配置、初始化基础设施、构造业务依赖、注册 server、启动应用、退出时清理资源。别让业务初始化藏在接口函数里，不然排查时得拿放大镜找。

## 适用场景

适用于所有需要独立进程部署的服务，尤其是同时暴露 HTTP/gRPC、依赖 DB/Redis/RPC client、需要注册服务发现的服务。

## 通用抽象

- `BootstrapConfig`：静态启动配置，包含 server、data、logger、trace、registry、runtime config 等。
- `ProviderSet`：按层声明构造函数集合，如 `server.ProviderSet`、`data.ProviderSet`、`biz.ProviderSet`、`service.ProviderSet`。
- `initApp`：由 DI 工具生成或手写，负责串联所有 provider。
- `newApp`：只负责把 server、logger、registrar 装进应用框架，不做业务初始化。
- `cleanup`：统一关闭 DB、Redis、文件句柄、埋点客户端等资源。

## 核心流程

1. 解析 `-conf` 参数，默认指向服务自己的 `configs` 目录。
2. 用配置框架加载 YAML 或目录配置，并扫描到 `BootstrapConfig`。
3. 初始化 logger 和 tracer，后续 provider 全部复用同一个 logger/tracer。
4. 构造注册中心、DB、Redis、RPC client、repository、usecase、service handler。
5. 构造 HTTP server、metrics server、gRPC server。
6. 创建应用对象并注册 server、registrar、metadata。
7. `Run` 阻塞到收到退出信号；退出时执行 cleanup。

## 可变点

- DI 可用 Wire、Fx、Dig、手写构造函数。
- `newApp` 可换成框架自己的 app/server group。
- `cleanup` 可以是一个函数，也可以用 lifecycle hook 管理。
- 本地开发可跳过注册中心，但要显式通过配置或环境变量控制。

## 落地模板

```go
func main() {
    cfg := LoadBootstrapConfig(flagconf)
    logger := NewLogger(cfg.Logger)
    tracer := NewTracer(cfg.Trace)

    app, cleanup, err := initApp(cfg.Server, cfg.Registry, cfg.Data, logger, tracer, cfg.Runtime)
    if err != nil {
        panic(err)
    }
    defer cleanup()

    if err := app.Run(); err != nil {
        panic(err)
    }
}
```


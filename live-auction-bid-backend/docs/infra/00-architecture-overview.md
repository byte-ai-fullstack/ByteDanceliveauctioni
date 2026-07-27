# 通用多服务后端架构

## 通用目标

把一个在线业务后端拆成职责明确、可独立发布、可观测、可横向扩展的服务组。文档里的名称都是抽象名，`GameService`、`AuthService`、`GatewayService`、`HeartbeatService` 分别代表核心业务、认证登录、入口发现和在线状态这几类职责。

## 适用场景

适用于移动游戏、实时互动产品、App 后台、带登录态和运行时配置的多服务系统。单体服务也能参考其中的启动、配置、日志、协议、数据层模式。

## 通用抽象

- `GatewayService`：给客户端返回区域、版本、更新、核心服务地址等启动信息。
- `AuthService`：负责登录、账号绑定、合规检查、版本和维护状态判断。
- `GameService`：承载主要业务 RPC/HTTP 接口，通常是协议和数据层最复杂的服务。
- `HeartbeatService`：维护在线状态、最后活跃时间、离线通知等轻量链路。
- `RuntimeConfig`：从配置中心加载可热更新的业务配置，并用本地备份保证启动可恢复。
- `Observability`：统一日志、指标、链路追踪和 panic recovery，不把排障能力散落在业务函数里。

## 核心流程

1. 客户端先请求 `GatewayService`，获得目标区域、CDN、登录服地址。
2. 客户端请求 `AuthService` 完成登录，拿到用户 ID、会话时间戳、核心业务服地址。
3. 客户端对 `GameService` 发起带 `RequestHead` 的业务请求，业务 payload 可加密封装。
4. 客户端周期性请求 `HeartbeatService`，服务端据此判断在线、离线和最近活跃。
5. 每个服务启动时加载静态配置、初始化 logger/tracer、注册 HTTP/gRPC server、注册服务发现。
6. 运行中通过配置中心热更新版本、维护、玩法、开关等运行时配置。

## 可变点

- RPC 框架可用 Kratos、grpc-go、Connect、Gin+gRPC 等替换。
- 服务发现优先使用部署平台原生 DNS；本地用 Docker DNS，生产用 Kubernetes Service DNS
- 数据层可按服务选择 GORM
- 配置中心可用 Apollo
- 观测系统可接 Prometheus

## Odin 参考实现

- `/home/dministrator/odin/app/gateway/service/internal/server/http.go`：`NewHTTPServer`
- `/home/dministrator/odin/app/thor/service/internal/server/http.go`：`NewHTTPServer`
- `/home/dministrator/odin/app/loli/service/internal/server/http.go`：`NewHTTPServer`
- `/home/dministrator/odin/app/heartbeat/service/internal/server/http.go`：`NewHTTPServer`
- `/home/dministrator/odin/app/loli/service/cmd/server/main.go`：服务启动、配置加载、依赖注入入口

## 落地模板

```text
/app
  /gateway/service
  /auth/service
  /game/service
  /heartbeat/service
/api
  /gateway/service/v1
  /auth/service/v1
  /game/service/v1
  /heartbeat/service/v1
/pkg
  /analytics
  /security
  /timer
  /discovery
```


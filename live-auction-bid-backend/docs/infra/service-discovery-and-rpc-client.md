# 服务发现与 RPC Client

## 通用目标

服务之间通过稳定的服务名调用，而不是硬编码实例 IP。线上使用 Kubernetes Service DNS，本地使用 Docker DNS，并允许显式直连 override。RPC client 默认带 timeout、trace、recovery，必要时加熔断。

## 适用场景

适用于多服务部署、服务动态扩缩容、本地开发和线上服务发现策略不同的系统。

## 通用抽象

- `PlatformService`：Docker/Kubernetes 提供的稳定服务名和负载均衡边界。
- `ServiceEndpoint`：形如 `dns:///auction-service:19090` 的逻辑地址。
- `DirectEndpointOverride`：本地开发或故障排查用的直连环境变量。
- `RPCClientFactory`：创建带 middleware、timeout、discovery 的 client。
- `ClientMiddleware`：tracing、recovery、circuit breaker、retry、auth metadata。

## 核心流程

1. 部署清单创建稳定 Service，Pod 仅声明 label、port 和健康探针。
2. 调用方默认使用 `dns:///auction-service:19090`。
3. 如果直连环境变量存在，则使用显式 endpoint，便于本地排障。
4. 构造 RPC client options：timeout、middleware、endpoint。
5. Dial 或健康预检失败直接启动失败，避免服务半可用。

## 可变点

- 生产固定使用 Kubernetes Service DNS；应用不再内置可切换注册中心。
- 本地直连可用环境变量、配置项、命令行 flag。
- 熔断只给不稳定或非关键链路加，不要所有内部 RPC 一刀切。
- 内部调用是否重试必须看接口幂等性。

## 落地模板

```go
func NewAuctionRPCClient(tracer Tracer) auctionv1.AuctionServiceClient {
    endpoint := "dns:///auction-service:19090"
    opts := []grpc.ClientOption{
        grpc.WithTimeout(10 * time.Second),
        grpc.WithMiddleware(
            tracing.Client(tracing.WithTracerProvider(tracer)),
            recovery.Recovery(),
        ),
    }

    if direct := os.Getenv("GAME_GRPC_ENDPOINT"); direct != "" {
        endpoint = direct
    }

    opts = append(opts, grpc.WithEndpoint(endpoint))
    conn, err := grpc.DialInsecure(context.Background(), opts...)
    if err != nil {
        panic(err)
    }
    return auctionv1.NewAuctionServiceClient(conn)
}
```

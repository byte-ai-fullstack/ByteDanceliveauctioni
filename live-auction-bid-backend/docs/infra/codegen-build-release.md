# 代码生成、构建与发布

## 通用目标

把 proto、HTTP binding、error code、conf schema、DI、OpenAPI、build、Docker release 都收敛成标准命令。新接口不是写完 proto 就完事，还得生成、编译、验证，不然就是半截工程，放线上容易挨打。

## 适用场景

适用于使用 protobuf 定义 API、生成 HTTP/gRPC server/client、使用 DI 生成代码、多服务统一构建发布的 Go 后端。

## 通用抽象

- `api`：外部协议 proto，生成 go、grpc、http、errors、openapi。
- `internal/conf`：服务内部配置 proto，生成配置结构体。
- `wire`：生成依赖注入代码。
- `generate`：运行 `go generate ./...`，生成框架 client 或其他派生代码。
- `build`：注入版本和构建时间，输出二进制。
- `release`：构建 Linux 二进制、Docker image、push registry。

## 核心流程

1. 修改 API proto 后运行 grpc/http/errors/openapi 生成。
2. 修改 `internal/conf/conf.proto` 后运行 conf 生成。
3. 修改 provider 或构造函数签名后运行 wire。
4. 修改生成指令或表驱动代码后运行 `go generate ./...`。
5. 构建时注入 `Version`、`BuildTime`。
6. 发布时先生成 Linux 二进制，再构建镜像并推送。
7. 所有生成命令统一 include `third_party` 和系统 protobuf include 根。

## 可变点

- Makefile 可替换为 Taskfile、Mage、Bazel、just。
- OpenAPI 可用 grpc-gateway、gnostic、buf。
- DI 可手写，不一定用 Wire；但生成链路仍要有固定命令。
- 发布可对接 Docker、Kubernetes、Helm、Argo CD、云构建。

## 落地模板

```makefile
.PHONY: api
api: grpc http errors conf openapi

.PHONY: wire
wire:
	cd cmd/server && wire

.PHONY: build
build:
	mkdir -p bin
	go build -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)" -o ./bin/ ./...

.PHONY: all
all: api generate wire build
```
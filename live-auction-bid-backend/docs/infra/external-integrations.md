# 外部系统集成

## 通用目标

把支付、登录、合规、云服务、IP 定位等外部依赖收敛成独立 client，并在服务启动时初始化。业务逻辑只调用接口，不直接拼第三方 HTTP 请求和密钥。

## 适用场景

适用于接入应用商店支付、第三方登录、实名认证、防沉迷、内容安全、对象存储、客服、地理定位等能力的系统。

## 通用抽象

- `PaymentClient`：处理支付验单、退款、订阅状态。
- `AuthProviderClient`：处理 Apple/Google/Firebase 等登录或 token 验证。
- `ComplianceClient`：处理实名、防沉迷、监管上报。
- `CloudStorageClient`：处理临时凭证、对象上传、日志归档。
- `TextModerationClient`：处理昵称、聊天、公告等文本审核。
- `IPLocator`：从请求上下文取 IP，再查国家、地区、私网段。

## 核心流程

1. 在静态配置 schema 中声明每个外部系统的最小必需字段。
2. 服务启动时创建 client；关键 client 初始化失败则启动失败。
3. client 内部统一设置 timeout、签名、重试、错误包装。
4. 业务层通过 repository/usecase 调 client，不直接读配置 secret。
5. 对可选外部依赖设置 skip/debug 开关，但生产默认必须安全。
6. 外部回调入口使用手写 JSON 或专门协议，保证字段兼容和签名校验。

## 可变点

- 不同区域可以启用不同合规系统或支付 provider。
- 外部 client 可按功能放在 `/pkg`，也可按服务私有放在 `internal/pkg`。
- 是否启动失败取决于依赖重要性：支付/登录通常必须，埋点/客服可降级。
- 第三方错误要包装上下文，但不能把密钥和完整 token 打到日志。

## 落地模板

```go
type ExternalClients struct {
    Payment    PaymentClient
    Auth       AuthProviderClient
    Compliance ComplianceClient
    Storage    CloudStorageClient
    IPLocator  IPLocator
}

func NewExternalClients(cfg ExternalConfig) (*ExternalClients, error) {
    payment, err := NewPaymentClient(cfg.Payment)
    if err != nil {
        return nil, fmt.Errorf("payment client: %w", err)
    }
    auth, err := NewAuthProviderClient(cfg.Auth)
    if err != nil {
        return nil, fmt.Errorf("auth client: %w", err)
    }
    return &ExternalClients{Payment: payment, Auth: auth}, nil
}
```


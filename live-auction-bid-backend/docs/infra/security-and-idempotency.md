# 安全校验与幂等

## 通用目标

防止伪造请求、重放请求、多端会话冲突、客户端时间异常和重复提交。安全链路要在业务逻辑前完成，失败时不能污染返回对象或数据状态。

## 适用场景

适用于客户端直连业务服、请求 payload 加密、防刷、防重复扣费/领奖/提交的系统。

## 通用抽象

- `SessionKeyDeriver`：根据客户端 ID、会话时间戳和服务端密钥材料推导请求密钥。
- `PayloadCipher`：对 payload 做加密/解密，可替换为 AES-GCM、ChaCha20-Poly1305 等。
- `SignatureVerifier`：用请求头、客户端快照、nonce 计算摘要并比对签名。
- `NonceStore`：以 `user_id + nonce` 做短 TTL 幂等锁和回包缓存。
- `SessionStore`：保存用户当前登录会话，用于多端登录控制。
- `ClockGuard`：限制客户端时间倒退、超前和乱序。

## 核心流程

1. 检查调试开关；只有明确允许时才能跳过签名或加密。
2. 校验用户 ID、session、nonce 等必填字段。
3. 根据请求头推导 payload 密钥并解密。
4. 用关键字段和客户端快照计算签名，和请求签名比对。
5. `NonceStore.SetNX` 成功才继续；如果 nonce 已存在，尝试返回缓存回包。
6. 校验 session 是否仍是当前登录态，防止多端旧会话继续写数据。
7. 校验请求时间和服务端时间差，必要时更新最后请求时间。
8. 业务成功后缓存当前 nonce 的加密回包，重复请求直接复用。

## 可变点

- 密钥推导算法和加密算法应按安全要求替换，文档不固定 Odin 的实现。
- 幂等缓存 TTL 应覆盖客户端重试窗口，不能无限保留。
- 支付类接口应使用外部订单号或事件 ID 做更强幂等，不只依赖 nonce。
- 内部 RPC 可关闭 payload 加密，但仍建议保留 trace、timeout 和鉴权。

## 落地模板

```go
func VerifyAndDecode(ctx context.Context, req *RequestEnvelope) (*DecodedRequest, error) {
    if err := nonceStore.SetNX(ctx, req.UserID, req.Nonce, retryTTL); err != nil {
        cached, cacheErr := nonceStore.GetReply(ctx, req.UserID, req.Nonce)
        if cacheErr == nil {
            return &DecodedRequest{CachedReply: cached}, ErrDuplicatedNonce
        }
        return nil, err
    }

    key := keyDeriver.Derive(req.ClientID, req.SessionTS)
    plain, err := cipher.Decrypt(key, req.Payload)
    if err != nil {
        return nil, err
    }
    if err := signature.Verify(req.Head, plain); err != nil {
        return nil, err
    }
    return &DecodedRequest{PlainPayload: plain, Key: key}, nil
}
```


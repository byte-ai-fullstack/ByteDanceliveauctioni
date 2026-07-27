# 统一请求与回包协议

## 通用目标

把所有业务接口收敛到统一外壳：请求头承载身份、时间、签名和客户端快照；payload 承载具体业务数据；回包统一返回服务器时间和业务 payload。这样 AI 写新接口时不用每个接口发明一套协议，别整得跟每个屋都有一把不同钥匙似的。

## 适用场景

适用于客户端请求频繁、需要加密签名、防重放、统一返回码、兼容多语言客户端的业务系统。

## 通用抽象(head参数可以变)

- `RequestHead`：包含 `client_id`、`user_id`、`client_ts`、`session_ts`、`sign`、`nonce`、`version`、`platform`、`seq`、`client_snapshot`。
- `EncryptedPayload`：业务请求的序列化 bytes，可按配置加密或明文。
- `ReplyEnvelope`：包含 `server_ts` 和 `EncryptedPayload`。
- `Data`：每个接口内部嵌套的业务消息，避免外层协议字段膨胀。
- `RetCode`：面向客户端业务分支的返回码；框架错误仍走 RPC/HTTP error。

## 核心流程

1. service handler 从 proto/json 请求复制 `RequestHead` 到 usecase 入参。
2. usecase 先执行统一校验：身份、session、签名、时间、nonce。
3. usecase 解密 `payload`，反序列化成当前接口的 `Data`。
4. 业务逻辑只处理明确类型的 `Data`，不直接解析外层协议。
5. 业务生成 reply `Data`，序列化并按协议加密。
6. 回包填入 `server_ts` 和加密后的 `payload`。

## 可变点

- payload 可用 protobuf、JSON、msgpack，但接口内类型必须明确。
- 加密可按环境关闭，关闭开关必须只在调试或内部链路可用。
- `RetCode` 适合业务可恢复状态；认证失败、维护、参数结构错误适合框架错误。
- `client_snapshot` 只放服务端校验或埋点需要的最小状态。

## 落地模板

```proto
message RequestHead {
  string client_id = 1;
  int64 user_id = 2;
  int64 client_ts = 3;
  int64 session_ts = 4;
  bytes sign = 5;
  string nonce = 6;
  string version = 7;
  string platform = 8;
}

message DoActionReq {
  RequestHead head = 1;
  bytes payload = 2;
  message Data {
    int32 target_id = 1;
  }
}

message DoActionReply {
  int64 server_ts = 1;
  bytes payload = 2;
  message Data {
    RetCode ret_code = 1;
  }
}
```


# 数据层、DB 与 Redis

## 通用目标

数据层负责连接、迁移、事务、缓存、外部 RPC client 持有和资源关闭。业务层只依赖 repository 接口，不直接拿 DB/Redis client 满天飞。

## 适用场景

适用于同时使用关系型数据库、Redis 缓存、服务间 RPC 的业务服务；也适合按领域拆分不同 ORM 的多服务系统。

## 通用抽象

- `Data`：持有 DB client、Redis client、RPC clients、区域信息、ID generator 等基础依赖。
- `NewDB` / `NewEntClient`：初始化数据库连接、日志等级、迁移和 tracing plugin。
- `NewRedisClient`：初始化 Redis 连接、读写超时、DB index、tracing hook。
- `Repository`：按领域封装 DB/Redis/RPC 读写。
- `TxExtractor`：从 context 里取请求级或批次级事务。
- `RedisNameHelper`：统一 key 拼接、hash tag、前缀裁剪、字段拆分。

## 核心流程

1. 根据静态配置创建 DB client，设置 SQL 日志等级和 tracing。
2. 启动时执行必要迁移；高风险生产迁移可改为独立 migration job。
3. 创建 Redis client 并挂 tracing hook。
4. 创建服务间 RPC client，注入 tracing、recovery、timeout。
5. 构造 `Data` 聚合对象，交给 repository。
6. repository 方法从 context 提取事务；没有事务时使用普通 DB。
7. Redis key 统一使用前缀和 helper 拼接，不在业务函数里散落字符串。
8. cleanup 关闭 DB、Redis、文件句柄，并记录关闭错误。

## 可变点

- DB 访问可按服务选 GORM
- AutoMigrate 适合开发和可控表；生产可改为 Atlas/Flyway/Liquibase。
- Redis key 可加入租户、区域、hash tag，但必须有统一命名规则。
- 缓存可以全局开关，也可以按请求 context 禁用。

## 落地模板

```go
type Data struct {
    db      *gorm.DB
    redis   *redis.Client
    authRPC AuthServiceClient
}

func (r *userRepo) db(ctx context.Context) *gorm.DB {
    if tx, ok := batchtx.TakeBatchTx(ctx); ok {
        return tx
    }
    if tx, ok := batchtx.TakeReqTx(ctx); ok {
        return tx
    }
    return r.data.db.WithContext(ctx)
}

func (h RedisNameHelper) CreateKey(parts ...any) string {
    return strings.Join(toStrings(parts...), ":")
}
```


# 静态配置与运行时配置

## 通用目标

区分“启动前必须确定的配置”和“运行中可热更新的配置”。静态配置决定服务能不能启动，运行时配置决定业务规则怎么变。混在一起就会出现改个活动表还得重启服务，活儿干得跟搬砖还得审批一样。

## 适用场景

适用于有多环境部署、多区域配置、版本维护开关、玩法表、风控开关、第三方接入参数的服务。

## 通用抽象

- `BootstrapConfig`：由 proto/schema 定义，来自 YAML、环境变量或配置文件目录。
- `RuntimeConfigClient`：连接 Apollo/Nacos/Etcd 等配置中心。
- `RuntimeConfigStore`：服务内存中的配置快照，用 `RWMutex` 或原子指针保护。
- `ConfigParser`：按 key 解析不同配置项，失败时记录错误并保留旧配置。
- `BackupConfigPath`：把配置中心下发的大配置落本地，供重启和排查使用。

## 核心流程

1. 服务启动先加载 `BootstrapConfig`，没有必需字段就直接失败。
2. 用启动配置初始化运行时配置客户端。
3. 首次启动拉取配置中心全量 key，逐个解析到 `RuntimeConfigStore`。
4. 注册 change listener，收到变更后只重载受影响的 key。
5. 大体积配置可使用 base64/gzip/tar/protobuf 包，加载后写本地备份。
6. 业务读取配置只通过 `RuntimeConfigStore.GetXxx()`，禁止直接访问配置中心。

## 可变点

- 小配置用 JSON/YAML，复杂表用 protobuf 或压缩包。
- 并发保护可用 `sync.RWMutex`、`atomic.Value`、不可变快照。
- 热更新失败策略通常是“保留旧值并报警”，不是 panic。
- 配置中心不可用时，可按业务要求选择本地备份启动或直接失败。

## 落地模板

```go
type RuntimeConfigStore struct {
    mu          sync.RWMutex
    maintenance map[string]MaintenanceRule
    version     map[string]VersionRule
}

func (s *RuntimeConfigStore) ReloadMaintenance(raw string) error {
    next := map[string]MaintenanceRule{}
    if err := json.Unmarshal([]byte(raw), &next); err != nil {
        return err
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    s.maintenance = next
    return nil
}
```


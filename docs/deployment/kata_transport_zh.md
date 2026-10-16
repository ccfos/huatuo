# Kata Guest 到 Host 的数据传输

Host 配置开启接收：

```toml
[GuestBridge]
Enabled = true
Port = 19705
MaxConnections = 512
MaxMessageBytes = 16777216
MaxInFlightBytes = 268435456
HandshakeTimeoutSeconds = 10
FirecrackerRoot = "/run/vc/firecracker"
```

保留 Host Pod 发现。Host 需要 VSOCK、Host `/proc` 中的运行时进程视图和
Firecracker 运行目录。QEMU 连接 Host CID 2；Firecracker 使用已有混合通道，
每个 Sandbox 对应 `root/kata.hvsock_19705`。Unix socket 的对端进程必须是该
Sandbox 的虚拟机进程，目录路径本身不能证明身份。Host Agent 需要处于能够看到
实际虚拟机 PID 的命名空间，运行目录不得向业务容器开放写权限。

Guest 配置开启发送：

```toml
Mode = "sandbox"

[GuestTransport]
Enabled = true
Port = 19705
MaxMessageBytes = 16777216
MaxPendingBytes = 67108864
IOTimeoutSeconds = 10
```

按 Guest 部署说明提供 `BAMAI_POD_NAME`、`BAMAI_POD_NAMESPACE`、
`BAMAI_NODE_NAME`。开启后，Guest 的 tracing 数据和独立 profiling 窗口改用
VSOCK，替代其本地文件和 Elasticsearch 保存；`--disable-storage` 仍然优先。
两类数据分别使用连接和有容量限制的队列。

Host 将事件、自动追踪、JSON 火焰图和快照结果交给原 tracing 存储，保留文件
保存和订阅能力。强类型 profiling 窗口交给原 profiling 存储；Host 需配置
Elasticsearch，否则拒绝此类结果。订阅和 CloudEvents 保留原有筛选规则。

两个开关默认关闭。模式、发送和接收配置变更都需要重启，在线更新会被拒绝。
两端端口需要一致，发送帧不得超过接收上限，总接收预算至少容纳一个最大帧。

传输尽力而为：未连接、队列满和消息过大会返回错误，断线会丢弃该连接队列并
重连，退出时在截止时间内发送剩余数据。发送成功不能证明 Host 已接收或持久化。
Guest 的同步查询可见性、请求结果发布标记不通过此通道提供；独立采样窗口可以
转发，不能将其解释为远端 API 任务已完成。

Host 根据运行时信息认证连接，再检查容器所属 Sandbox 和命名空间。覆盖 Guest
自报的 Host 身份和容器元数据；VM 级结果只保留认证后的命名空间，不保留伪造的
容器字段。采集时间保留，上传时间由 Host 存储赋值。

真实通道测试入口为 `internal/guestbridge/testdata/sender` 和 integration 标签的
`TestKataVSOCKIntegration`，需要显式提供测试容器快照、端口、就绪文件等
`HUATUO_KATA_BRIDGE_*` 环境变量。应使用可清理的临时 Kata Pod。性能采样存储
测试使用 HTTP 测试服务验证原有编码和批量写入，不代表已验证真实 Elasticsearch。

`FirecrackerRoot` 必须与实际运行目录一致，例如较新 Kata 安装可能使用
`/run/kata/firecracker`。

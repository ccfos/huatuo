#### 特性

- 支持网卡硬件状态指标，更多字段，如驱动，驱动版本，固件版本
- 支持网卡硬件丢包指标
- 支持网卡 PFC 指标
- 支持 perf 火焰图生成，应用于 autotracing
- 支持 arm64 处理运行
- 支持指标、事件、 追踪 region 字段
- 支持 softirq percpu 指标
- 支持 golangci 静态检查
- 支持组件的 cgroupv2 资源限制
- 支持独立的 cgroup 包, 应用无感知 cgroup 运行时类型
- 支持根据 kubelet cgroupdriver 配置，实现 cgroupfs, systemd cgroup 路径转换
- 支持更多命令启动参数

#### BUG 修复

- 若干代码优化和 BUG 修复
- 新增完善若干文档

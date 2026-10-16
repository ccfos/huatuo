# Host 上识别 Kata 容器

Host 发现使用已配置的 containerd CRI 地址。运行中容器的详细状态必须提供
`runtimeType`、有效的 Host 虚拟机进程 `pid` 和 `sandboxID`。支持
`io.containerd.kata.v2` 及 `io.containerd.kata-*.v2`，包括 QEMU、Firecracker。

Agent 需要能够读取 Host 的 proc 和 cgroup。CPU 路径来自虚拟机进程的真实资源分组，
再用目录 inode 匹配 BPF 上报的 CPU CSS。CSS 缺失或存在冲突时，沿用现有控制器重试，
不会误用额外开销目录的状态。虚拟机的网络命名空间不作为 Guest 容器的网络身份。
Host Kata 元数据增加 `runtime_type` 和 `sandbox_id`；普通容器识别运行时后仍沿用
原有 PID 文件处理。

cgroup v1/hybrid 下，CPU、CPU 记账和内存的成员路径必须一致，因为当前容器元数据
只有一个共享资源路径；缺少 CPU 记账或配额文件时会拒绝该绑定。v2 需要 `cpu.stat`
和 `cpu.max`。可选的 `cpu_weight_nice_read_s64` 探针用于刷新只有 CPU 控制器的目录，
缺失时保留已有生命周期发现能力。

这里描述 Host 元数据和 CPU 归属，Guest 数据传输属于独立功能。
Guest 启动参见 [Guest 部署](kata_guest_zh.md)。

## 对现有 Host 做只读验证

先执行 `make build`，再选择已经运行的 Kata 容器：

```sh
HUATUO_BPF_OBJECT_DIR="$PWD/bpf" \
HUATUO_KATA_CONTAINER_ID=<完整容器ID> \
go test -tags integration ./internal/pod \
  -run TestKataRuntimeIntegration -v -count=1
```

测试会临时挂载 BPF 探针、读取 CRI/proc/cgroup，并在结束时释放探针，
不会重启或删除所选容器。QEMU 和 Firecracker 分别验收，不能互相替代。

## 共享虚拟机的 CPU 归属

同一 Kata Sandbox 内的容器共享 Host CPU CSS。选择最早发现且仍存活的成员代表该
虚拟机，发现时间相同时按容器 ID 决定；元数据刷新保留原发现时间。代表退出后由
下一个存活成员接替。运行队列延迟仅由代表输出，避免重复或使用其他成员的旧缓存。

CPU 自动采样按 Sandbox 保存状态，先应用容器过滤条件再选择代表，结果中增加
`sandbox_id`。代表切换保留用量历史和采样冷却时间；虚拟机 CPU CSS 或资源路径改变
时重建用量基线并保留冷却时间；新 Sandbox 使用新状态。Host Kata 绑定随容器定期
刷新重新检查；读取 CPU 状态产生的缓存更新不会再触发一轮生命周期刷新。

如需接收并认证 Guest 的事件与性能采样结果，见[Guest 到 Host 的数据传输](kata_transport_zh.md)。接收端默认关闭。

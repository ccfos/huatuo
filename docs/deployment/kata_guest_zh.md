---
title: Kata Guest 采集器
type: docs
weight: 30
---

huatuo-bamai 可以在 Kata 虚拟机内以特权 sidecar 运行。Pod 必须
让采集器看到 Guest 内完整的业务进程和 cgroup 树。进程被隔离时需要共享
Pod 的进程命名空间，但 `shareProcessNamespace: true` 本身不能让私有 cgroup
挂载看到其他容器目录。部署时还需通过经过核对的 Kata 配置提供完整 Guest 视图。
不要把物理宿主机的 proc 或 cgroup 目录挂进 sandbox 模式的采集器。

## 配置与启动

不填写 Mode 或使用 `Mode = "none"` 时保持已有启动行为；`baremetal` 用于
宿主机，`sandbox` 在 Guest 内使用本地进程和 cgroup 发现，不访问 kubelet
或读取 Host 的运行时 PID 文件。Mode 不能通过配置 API 在线切换，修改后需重启。

使用 `huatuo-bamai-sandbox.conf`，替换其中的 API token，并通过 Kubernetes
Downward API 或等效方式设置：

| 环境变量 | Kubernetes 字段 |
| --- | --- |
| BAMAI_POD_NAME | metadata.name |
| BAMAI_POD_NAMESPACE | metadata.namespace |
| BAMAI_NODE_NAME | spec.nodeName |

```sh
huatuo-bamai --region YOUR_REGION --config-dir /path/to/conf \
  --config huatuo-bamai-sandbox.conf --bpf-dir /path/to/bpf \
  --tools-bin-dir /path/to/bin
```

Guest 需要允许加载 BPF 和挂载 tracefs/debugfs。已有挂载直接复用，缺失的
挂载在 BPF 初始化前准备，并兼容 debugfs 下的旧 tracing 布局。内核不支持
tracing 时记录警告，本地容器发现仍可运行，不依赖 BPF 探针就绪。CO-RE 采集器
还需要 Guest 内核提供 BTF；采集器主进程启动成功不代表各 BPF 采集模块通过验证。API 认证要求保持不变，示例 token 必须替换并妥善保管。

默认不创建采集器自身的资源 cgroup。显式指定 `--enable-cgroup` 后，如果
Guest 缺少 systemd 而创建失败，sandbox 模式记录警告并继续运行；宿主机模式仍报错。

## 容器发现与生命周期

每五秒扫描一次可见进程，按容器 ID 分组，排除采集器自身容器，优先选择父进程
不属于本容器的进程。通过进程启动时间和内存 cgroup 目录识别容器实例，PID
被复用时生成新的生命周期标识。扫描不完整时保留已知容器，完整扫描确认退出后删除。

支持 cgroup v2 以及 CPU 和内存目录分开的 cgroup v1。只有 `/../容器ID`
等命名空间相对路径才会查找实际目录，而且必须唯一匹配；已有有效绝对路径不被替换。
结果进入现有容器 API 和订阅机制，Pod 名称和命名空间来自配置的 Guest 身份。

## 当前数据去向

本配置将 Guest 结果按当前文档格式保存到本地，节点名称取 BAMAI_NODE_NAME。
这里不启用 Guest 到 Host 的 VSOCK 转发；通信和 Host 侧连接身份认证单独实现。
检查 Guest 本地结果时仍需使用现有 API 认证。

如需开启 VSOCK 转发，见[Guest 到 Host 的数据传输](kata_transport_zh.md)，其中说明 Host 身份校验、队列限制和数据丢失语义。默认配置仍保存本地结果。

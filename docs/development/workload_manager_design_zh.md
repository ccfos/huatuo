---
title: Pod 多来源发现与资源采集重构方案
type: docs
description: 统一容器与沙箱发现，拆分资源绑定、实例管理和公共采集。
author: HUATUO Team
date: 2026-11-04
---

# Pod 多来源发现与资源采集重构方案

将 `internal/pod` 从依赖容器运行时的管理模块，调整为支持替换发现来源的 Workload Manager。每个 agent 实例只启用一个来源，不同时汇总多个来源。来源负责发现对象和提供资源定位信息；公共层负责同步、缓存、实例识别、资源绑定与通知；采集器根据已绑定的资源工作。

本文记录已讨论的架构与 CubeSandbox 接入约定，作为后续实现依据。接口中尚未确定的细节列在文末；本文描述目标行为，不代表功能已经实现。

## 范围与对象粒度

- 保留 Container 来源的 Kubernetes 容器发现能力，以及已有 Kata Host 和 Guest 采集行为。
- 增加 CubeSandbox 来源，一条 `Pod` 对应一个沙箱，采集它在宿主机上的整体资源。
- 本版同时接入 CubeSandbox 的 Guest 采集与 Host 侧数据接收、身份关联。Guest 事件、tracing 及 profiling 统一归属整个沙箱，结果保留进程信息，本版不做 CubeSandbox Guest 指标，也不新增 Guest 内每个容器或应用的独立发现与生命周期管理。
- Guest 内数据由 Guest 侧采集，不把宿主机 VMM 的指标或 profiling 当成 Guest 数据；现有 Kata Guest 的容器发现与采集行为保持兼容。
- 为 OpenSandbox 保留接入位置。协议未确定前不实现查询，也不注册返回空列表的占位来源。
- 保留 `pod` 包名和 `Pod` 类型名。这里的 `Pod` 表示统一监控对象，不等同于 Kubernetes Pod；Container 来源仍以原来的单个容器为粒度。

本次重构不要求所有对象都有进程和网络绑定。只有 cgroup 的沙箱，也应能够进行对应的 cgroup 指标采集。

这一版以简单实现为目标：不增加多来源并行调度、跨来源资源去重，以及目录编号复用或同一对象资源替换的专项处理。保留已有实例识别机制，不增加独立的绑定代次、目录句柄保留或按资源重置采集状态的框架。

## 现有实现与拆分位置

| 现有代码 | 当前职责与耦合 | 目标位置 |
| --- | --- | --- |
| `container_controller.go` | 同步、重试与 kubelet 查询、Kubernetes 类型混合 | 公共调度放入 `manager.go`，容器发现放入 `container/` |
| `container_events.go` | 缓存、实例识别、订阅队列集中在一起 | 拆入 `store.go`、`subscription.go` |
| `container_cgroup_path.go` | 容器路径解析、内核绑定校验与运行时查询混合 | 来源规则放入 `container/cgroup.go`，公共绑定放入 `cgroup.go` |
| `cgroup_lifecycle.go` | 从 cgroup 名称提取容器 ID，再触发刷新 | 公共事件按资源关联；容器命名规则留在来源内 |
| `container_life_resources.go` | 保存容器生命周期内的采集状态 | 迁移到 `resources.go`，按对象实例管理 |
| `guest_scanner.go` | Guest 内扫描，复用当前容器缓存与通知 | 保留扫描能力，接入统一发现约定 |

现有完整快照、未解析成员保留、实例代次、有界订阅队列等机制可以复用，不需要另建一套管理框架。

## 架构与目录

```text
Container 来源     CubeSandbox 来源     OpenSandbox 来源     后续来源
      │                  │                   │                 │
      └──────────────────┴───────────────────┴─────────────────┘
                                  │
                        选择一个来源的发现结果
                                  │
                                  ▼
                          Workload Manager
                     同步、缓存、实例识别、通知
                      cgroup / 进程 / 网络关联
                                  │
                                  ▼
                      按资源工作的公共采集器
                    cgroup 指标、网络、事件、profiling
```

```text
internal/pod/
├── workload.go              # 公共结构体、查询方法
├── source.go                # 发现来源接口
├── manager.go               # 同步调度、重试
├── store.go                 # 缓存、实例识别、新旧状态比较
├── subscription.go          # 生命周期通知
├── cgroup.go                # 公共 cgroup 绑定和事件归属
├── resources.go             # 随实例保存的采集状态
│
├── container/
│   ├── source.go            # Container 发现实现
│   ├── kubelet.go           # kubelet 查询
│   ├── runtime.go           # Docker/containerd 信息解析
│   ├── cgroup.go            # 容器 cgroup 路径解析
│   └── metadata.go          # 容器类型、QoS 等解析
│
├── cubesandbox/
│   ├── source.go            # CubeSandbox 发现实现
│   └── client.go            # cubelet.sock 查询
│
└── opensandbox/
    ├── source.go            # OpenSandbox 发现实现
    └── client.go            # 协议待确定
```

这是目标目录，OpenSandbox 文件在协议确定后创建。已有 Guest 扫描及身份处理代码需要保留，其最终文件位置在迁移时确定，不因目录调整删除现有能力。

各来源子包可以依赖 `pod` 定义的公共结构和接口，`pod` 不反向导入来源子包。启动入口负责创建选定的一个来源并注入 Manager，避免循环依赖。Guest 模式只在本机采集，不同时启用宿主机查询；现有 Kata 使用本地容器扫描，CubeSandbox Guest 按整机粒度采集。

## 来源配置

通过配置显式选择来源，沿用现有 `[Pod]` 配置段，增加 `Source` 字段：

```toml
[Pod]
    Source = "container"
```

默认值为 `container`，保持现有部署行为；CubeSandbox 宿主机配置为 `cubesandbox`。宿主机启动时只创建选定来源，不自动探测，也不在查询失败后切换来源。`opensandbox` 在实现前不可启用，未知或未实现的来源应在启动时明确报错。

发现启用时，现有 Kata Guest 继续使用本地扫描；CubeSandbox Guest 不新增内部应用发现。两者均不查询宿主机来源，CubeSandbox Guest 的数据身份由 Host 核验连接后补齐，具体约定见 Guest 接入部分。

`Mode` 和 `Pod.Source` 共同决定启动行为：

| 运行位置 | `Pod.Source` | 行为 |
| --- | --- | --- |
| Host，`Mode` 非 `sandbox` | `container` | 启用 Container 来源，沿用现有发现行为 |
| Host，`Mode` 非 `sandbox` | `cubesandbox` | 启用 CubeSandbox 来源，通过 cubelet 发现沙箱 |
| Guest，`Mode = "sandbox"` | `container`（默认） | 沿用现有 Kata Guest 本地容器扫描和身份配置要求 |
| Guest，`Mode = "sandbox"` | `cubesandbox` | 仅在 Guest 内做整机采集，不查询 cubelet，不启动内部应用发现 |

上表中的发现行为受统一禁用开关控制。Guest 的 `Pod.Source` 用于选择对应的本地采集行为，不表示在 Guest 内创建宿主机查询客户端。

统一使用 `--disable-workload-discovery` 关闭 workload 发现。该开关对 Container、CubeSandbox 和 Guest 本地扫描全部生效：关闭后不启动 Manager，宿主机采集继续。选用哪种来源与是否启用发现分别由 `Pod.Source` 和此开关控制。

旧的 `--disable-kubelet` 保留为兼容别名，与新开关行为一致，并在命令行帮助和配置文档中标记废弃。明确接受其在 Guest 模式下的行为变化：旧实现会忽略该开关并继续本地扫描，统一后也会关闭 Guest 本地扫描。

GuestBridge 依赖发现结果，开启 GuestBridge 却关闭 workload 发现时启动报错，不启动缺少发现能力的 GuestBridge。

`Pod.Source` 和 cubelet socket 地址只在启动时读取，修改后重启 agent 生效。运行时配置接口拒绝修改这些字段并提示需要重启，不更新内存配置或通过该接口持久化变更；不增加运行中的来源切换、连接重建和订阅迁移逻辑。

## 公共对象与字段归属

```go
const (
	TypeContainer   = "container"
	TypeCubeSandbox = "cubesandbox"
	TypeOpenSandbox = "opensandbox"
)

type Pod struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`

	Cgroups CgroupBinding `json:"cgroups"`
	Process *HostProcess  `json:"process,omitempty"`
	Network *HostNetwork  `json:"network,omitempty"`

	StartedAt time.Time      `json:"started_at"`
	SyncedAt  time.Time      `json:"synced_at"`
	Labels    map[string]any `json:"labels"`

	Container *ContainerInfo `json:"container,omitempty"`

	lifeResources map[string]any
}
```

`Type` 表示来源类别。原有 normal、sidecar、QoS 等容器属性进入 `ContainerInfo`，不与公共类型混用。公共采集器根据资源是否可用选择对象，不以 `Container != nil` 作为采集前提；原有容器筛选行为在迁移中保持。

| 数据 | 填写方 | 约定 |
| --- | --- | --- |
| 身份、名称、标签、启动时间、容器元数据 | Source | 仅填写来源能确认的信息 |
| cgroup 路径、代表进程、网络定位信息 | Source | 提供资源位置，不保证本机资源已经就绪 |
| 本机资源校验、CSS 与进程、网络关联 | 公共绑定层 | 不调用 kubelet 或 cubelet 猜测对象身份 |
| `SyncedAt` | Manager | 成功提交该条发现记录的时间；查询失败或 nil 记录不刷新 |
| 实例代次、目录身份、绑定有效性 | Store 内部记录 | 不作为来源字段，也不进入 `Pod` JSON |
| `lifeResources` | Manager 与采集器 | 保存实例内采集状态，来源不能初始化或复用它 |

Source 返回待绑定的 `Pod`，Manager 接管后完成绑定并发布只读视图。来源不得在返回后继续修改对象、切片或 map。元数据更新通过新视图提交，不能原地修改读者正在使用的数据。`lifeResources` 中可变状态的并发访问由相应采集器负责。

公共资源结构及迁移时的兼容处理见文末“实施接口细化”。

## 发现接口与完整性

```go
type Source interface {
	Type() string
	Discover(ctx context.Context) (Discovery, error)
}

type Discovery struct {
	Pods       map[string]*Pod
	Complete   bool
	GuestPeers map[string]GuestPeer
}

type GuestPeer struct {
	PID int
}
```

第一版采用周期性全量发现，默认每 30 秒同步一次，并允许通过配置调整周期。Source 不必实现 watch、内部调度或后台重试。Manager 控制调用超时、同步周期和失败重试。

同步周期决定发现新对象和更新其状态的时效，不改变指标采样周期。仅靠周期同步时，新沙箱可能等待约一个同步周期加查询耗时才开始采集；寿命短于周期的沙箱可能未被发现。本版接受这一时效边界，不为捕获所有短生命周期沙箱增加实时发现机制。

`Pods` 按 ID 索引。非 nil 记录的 `Pod.ID` 必须与 map key 相同，`Pod.Type` 必须与 Source 一致。一个 Manager 只接收一个 Source，不维护来源注册表或跨来源合并逻辑。

`Complete` 只说明成员列表完整，不说明每个对象都已解析或绑定成功。已发现 ID 但本次详情无法转换时，使用 nil 值保留该成员。新成员的 nil 记录不产生可采集对象，已有成员也不能因此被误删。

| 结果 | 提交与删除规则 |
| --- | --- |
| 完整列表且记录有效 | 更新记录，删除该来源中不在列表内的对象 |
| 完整空列表 | 删除该来源的全部对象 |
| 完整列表中存在 nil 记录 | 保留这些 ID 的成员身份；仍可删除其他缺失对象 |
| 不完整列表 | 可提交明确有效的记录，不根据缺失项删除对象 |
| 查询失败且无有效记录 | 保留已有对象，记录来源错误并重试 |

`error` 描述查询或转换失败，删除权限由 `Complete` 决定。即使部分记录转换失败，只要成员 ID 全部确定，仍可返回 `Complete: true`；如果损坏记录连 ID 都无法确认，则必须返回不完整结果。重复或冲突 ID 不能通过 map 覆盖静默处理，应报告错误并避免据此作出删除判断。

## CubeSandbox 来源

### 已确认的接口语义

cubelet 提供一次列表查询，不需要列表后逐个查询。当前实现最多返回 5000 条，不能无条件视为完整列表。开启 `private_with_cubebox_store: true` 后可以看到 `CGroupPath`。

通过 Unix socket 上的 gRPC 调用，完整方法名为 `/cubelet.services.cubebox.v1.CubeboxMgr/List`。Source 不传 `id`，一次获取全部沙箱信息，请求参数如下：

```json
{
  "option": {
    "private_with_cubebox_store": true
  }
}
```

从返回结果的 `items[].private_cubebox_storage_data` 读取每个沙箱的详情。该字段在 protobuf 中是 `bytes`，服务端将内部存储对象序列化为 JSON 放入其中；Go 客户端拿到 `[]byte` 后直接解码 JSON，不需要额外 base64 解码。上面的 JSON 表示请求参数，不表示使用 HTTP JSON 接口。

默认 socket 为 `/data/cubelet/cubelet.sock`，地址应允许配置。客户端复用连接并为每次 RPC 设置超时；不通过 shell 调用 cubecli。

### 代码核对结果

以下结果来自本地 CubeSandbox 源码，核对时 HEAD 为 `3028bca5`。表中的路径相对于 CubeSandbox 仓库，不是 HUATUO 仓库。

| 位置 | 已确认行为 |
| --- | --- |
| `pkgs/proto/services/cubebox/v1/cubebox.proto` | 定义 `CubeboxMgr.List`，响应仅有 `items`，没有分页或截断标志，也没有业务 `ret` 字段 |
| `pkgs/proto/services/cubebox/v1/cubebox_grpc.pb.go` | 已有生成的 `NewCubeboxMgrClient` |
| `Cubelet/services/cubebox/service.go` | List 从存储取记录，超过 5000 条时先截断，再应用请求过滤条件；不传过滤条件时会包含非运行态记录 |
| `Cubelet/services/cubebox/service.go` 的 `toGRPCCubeBox` | `items[].id` 来自 `box.ID`；私有详情序列化失败只记日志，条目仍返回但详情可能为空 |
| `Cubelet/services/cubebox/cube_container_create.go` | 创建时 `ID`、`SandboxID` 使用相同的沙箱 ID；`StartedAt` 使用 `time.Now().UnixNano()` |
| `Cubelet/pkg/store/cubebox/status.go` | 状态区分创建、运行、暂停中、暂停、退出和未知；暂停标记优先于退出时间；`Unknown` 不进入私有 JSON |
| `Cubelet/services/cubebox/update.go`、`rollback.go` | 恢复和回滚可以保留非零 `StartedAt`，不能把它当成每次 VM 重建都会变化的唯一标识 |
| `Cubelet/config/config.toml`、`cmd/cubelet/config.go` | 默认 gRPC socket 为 `/data/cubelet/cubelet.sock` |

因此，Source 可以直接使用外层 `items[].id` 确定成员身份。私有详情为空或损坏时保留该 ID 的 nil 记录，不因为单条 JSON 解码失败而丢失成员身份。有效详情只解码本次需要的字段，不依赖 Cubelet 的完整内部存储类型。

5000 条限制影响 `Discovery.Complete`：在原始 `items` 数量达到 5000 时返回 `Complete: false` 和明确错误，更新能够解析的记录，但不根据缺失项删除旧对象。这个判断必须在本地状态过滤之前进行，因为非运行态记录也占用上限；本版不修改 cubelet，也不扩展服务端分页。

列表不是运行中对象清单。第一版只采集 `CONTAINER_RUNNING` 状态的沙箱，使用响应中代表沙箱的容器状态判断，不根据任意一个 Guest 应用容器的状态判断。不能仅根据私有 JSON 的 `StartedAt` 和 `FinishedAt` 推算，因为 `Unknown` 不序列化且暂停优先于退出。

| 状态或解析结果 | 发现与采集行为 |
| --- | --- |
| 明确运行中 | 纳入监控集合；资源未就绪时保留对象，等待后续绑定 |
| 明确创建中、暂停中、暂停、退出或 `CONTAINER_UNKNOWN` | 不纳入本次监控集合；通过完整发现结果移除原有采集对象 |
| 后续恢复运行 | 重新加入监控集合并初始化采集状态，不伪造来源启动时间 |
| 成员 ID 已知，但必要字段缺失或解析失败 | 按未解析成员处理，不能当作明确退出；保留已有成员并等待后续同步 |

`Discovery.Complete` 表示监控范围内成员是否完整，而非要求返回 cubelet 中所有历史记录。明确排除非运行态不使结果变为不完整，但 List 的 5000 条上限必须在状态筛选前检查。本版不增加独立的暂停生命周期管理。

复用独立的 `github.com/tencentcloud/CubeSandbox/pkgs/proto` 模块，不引入整个 Cubelet。该模块声明 Go `1.25.7`，已决定允许将 HUATUO 当前的 Go `1.24.3` 构建版本升级到 `1.25.7` 或兼容的更高版本，并同步更新构建环境和 CI。依赖固定到可获取的模块版本或提交，不把本地 checkout 作为发布依赖，也不另行复制维护 protobuf 协议代码。实施时验证工具链与现有依赖、构建流程的兼容性。

`status.Status.Pid` 是 cube-shim 进程。样例中的 `endpoint.Pid` 与其相同，不能据此当作 VMM PID，也不能从该进程的 cgroup 或网络命名空间推导沙箱资源。

`CGroupPath` 指向沙箱整体的宿主机资源。目录编号由系统分配，可能复用，不能作为沙箱身份。相关字段示例如下，省略了本次不使用的接口字段：

```json
{
  "ID": "42216e9f562a484fa1214c784b95e65c",
  "name": "",
  "sandbox_id": "42216e9f562a484fa1214c784b95e65c",
  "namespace": "default",
  "IP": "172.16.0.2",
  "CGroupPath": "/cube_sandbox/sandbox/10",
  "endpoint": { "Pid": 242835 },
  "status": {
    "Status": {
      "Pid": 242835,
      "StartedAt": 1793621483121629407,
      "FinishedAt": 0
    }
  }
}
```

### 字段转换

| cubelet 字段 | 目标 | 规则 |
| --- | --- | --- |
| 外层 `items[].id` | `Pod.ID` | 使用服务端存储主键；创建路径中与私有详情的 `ID`、`sandbox_id` 一致 |
| 固定值 | `Pod.Type` | `TypeCubeSandbox` |
| `name` | `Pod.Name` | 为空时使用沙箱 ID，不依赖 `first_container_name` |
| `namespace` | `Pod.Labels` | 保存沙箱命名空间，指标输出为 `workload_namespace`；内部 label key 在实现时统一定义 |
| `CGroupPath` | `Pod.Cgroups` | 按本机 v1/v2 布局绑定 |
| `status.Status.StartedAt` | `Pod.StartedAt` | Unix 纳秒，按整数转换；恢复和回滚不保证更新，不作为 VM 重建的唯一标识 |
| `status.Status.Pid` | GuestBridge 连接核验信息 | 用于核对 Unix 连接对端 PID，不填入 `Pod.Process` |
| `IP` | 网络地址元数据 | 不能单凭地址宣称网络采集已就绪 |

CubeSandbox 的 `Container` 和 `Process` 初始为空。endpoint、端口映射和组件版本暂不加入公共模型。数字时间字段应按整数读取，避免经浮点数转换丢失精度。

未返回 `CGroupPath` 时仍保留发现结果，但 cgroup 处于未绑定状态；不能退回容器路径拼接，也不能使用 shim PID 补齐。整体 JSON 解析失败时返回不完整结果，不能返回完整空列表。

## CubeSandbox Guest 接入

CubeSandbox Guest 接入属于本版范围。复用现有 Guest 采集和 GuestBridge 能力，并补齐 CubeSandbox 的传输与身份关联；不能仅因选择 `cubesandbox` 来源就拒绝启用 GuestBridge。关闭 workload 发现时与 GuestBridge 的冲突校验仍然适用。

继续核对本地 CubeSandbox 源码得到以下事实，路径相对于 CubeSandbox 仓库：

| 位置 | 已确认行为与设计影响 |
| --- | --- |
| `CubeShim/shim/src/common/utils.rs` | VSOCK 基础路径为 `/run/vc/vm/<sandbox_id>/cube.sock`，Guest CID 配置为 `3`；不能单靠 CID 区分不同沙箱 |
| `hypervisor/virtio-devices/src/vsock/unix/muxer.rs` | Guest 连接 Host 端口时，转接到基础路径追加 `_<port>` 的 Unix socket，可复用现有 GuestBridge 的按沙箱监听方式 |
| `CubeShim/shim/src/hypervisor/cube_hypervisor.rs` | cube-shim 内创建 `cube_hypervisor::VmmInstance`，VMM 为嵌入式实现；shim PID 可作为连接来源核验的候选依据，不能因其名称直接排除 |

传输对端核验与采集资源绑定分开处理：保留 `Process` 初始为空、不从 shim 的 cgroup 或网络命名空间推导沙箱资源的约定。GuestBridge 使用来源提供的 shim PID 核验连接，具体规则如下；连接核验信息在 Source、Manager 与 GuestBridge 之间的传递接口仍需细化，并通过实际连接验证。

### Host 部署要求与连接核验

Host agent 直接运行在宿主机，或以启用 `hostPID` 的容器运行。Unix 连接对端 PID 与 cubelet 返回的 shim PID 必须使用同一宿主机 PID 命名空间，本版不增加跨 PID 命名空间转换。

部署需要允许 Host agent 访问 cubelet socket，并在沙箱运行目录 `/run/vc/vm/<sandbox_id>/` 下创建接收 socket。容器部署需挂载实际的宿主机运行目录，使 VMM 能访问 Host agent 创建的 socket；只有容器私有目录中的同名路径不满足这一要求。

Host 按沙箱运行目录监听 `cube.sock_<port>`，依据该监听路径确定候选沙箱，再读取 Unix 连接的 `SO_PEERCRED`，将对端 PID 与 cubelet 为该沙箱返回的 shim PID 核对。候选对象、对端凭据或有效 shim PID 无法确认，或 PID 不匹配时，不接收该连接的数据。不单凭目录名、Guest 自报身份或固定 Guest CID 接受连接。

这些信息仅用于确认连接归属，不因此将 shim 设为公共采集代表进程，也不改变已有 Kata GuestBridge 的身份核验规则。实施时验证宿主机进程和 `hostPID` 容器两种部署下的实际对端 PID、目录挂载与 socket 可达性。

### Guest 采集粒度与数据归属

本版 CubeSandbox Guest 按整机粒度采集事件、tracing 及 profiling，数据统一关联所属沙箱的 `workload_id` 和 `workload_type`。结果保留进程信息，用于分析 Guest 内的具体进程，但不因此为每个内部容器或应用创建独立 `Pod`、发现来源或生命周期状态。现有 Kata Guest 的容器行为保持不变。

CubeSandbox Guest 指标暂不纳入本版，不增加 Guest 指标的采集输出、VSOCK 指标传输、Host 指标缓存或在宿主机 `/metrics` 上汇总 Guest 指标的能力。宿主机侧根据沙箱 cgroup 采集和输出指标的范围保持不变；已有采集器为事件或 autotracing 触发而读取本地数值的需求，不等于新增对外指标输出。

Host 和 Guest 数据使用同一组 workload 身份关联同一沙箱，同时必须明确区分采集位置。Guest 的 PID、cgroup 和网络信息仅表示 Guest 内部资源，不能据此绑定或查询宿主机资源。Guest 整机采集不依赖扫描出符合现有容器 ID 规则的应用容器。

HUATUO 当前 GuestBridge 接收 tracing/event 和 profiling 文档，CubeSandbox Guest 复用该文档上报通道。现有 Guest 本地扫描依赖容器 ID 识别及 Pod 名称、命名空间配置，CubeSandbox Guest 不沿用这一前置条件，按下述方式补齐数据身份。数据来源标记按下文存储映射中的兼容约定处理。

### Guest 身份初始化与 Host 归属

CubeSandbox Guest 无需预先配置沙箱 ID、名称或命名空间，也不强制要求现有 Kubernetes 身份环境变量 `BAMAI_POD_NAME`、`BAMAI_POD_NAMESPACE` 和 `BAMAI_NODE_NAME`。Guest 镜像可以复用，无需为每个沙箱单独注入身份；现有 Kata Guest 的配置要求保持兼容。

Guest 负责采集并上报本机数据，Host 根据经过核验的连接确定所属沙箱，再从 Manager 补齐 `workload_id`、`workload_type`、`workload_name` 和 `workload_namespace`。最终归属以 Host 确认的信息为准，不以 Guest 文档中自行声明的沙箱身份作为连接归属依据。

无法确认连接归属时，不接收该连接的数据，也不能将其按无归属宿主机数据保存。连接核验遵循上面的 Host 部署要求与 Unix 对端 PID 校验规则。

### Guest 启动配置

CubeSandbox Guest 复用 `Mode`、`Pod.Source` 和 `GuestTransport` 配置，以下片段表示在 Guest 内整机采集并通过 VSOCK 上报，其他常规配置继续沿用现有要求：

```toml
Mode = "sandbox"

[Pod]
Source = "cubesandbox"

[GuestTransport]
Enabled = true
```

该组合不连接 cubelet，也不启动内部应用发现。现有 Kata Guest 继续使用默认的 `Source = "container"`，保持原行为。Host 接收端使用自己的 CubeSandbox 来源配置并启用 `GuestBridge`，负责发现与核验身份；Guest 不需要配置 cubelet socket。

本版 CubeSandbox Guest 的数据只支持经 Host 保存。正常启用存储时，`Mode = "sandbox"` 且 `Pod.Source = "cubesandbox"` 必须同时配置 `GuestTransport.Enabled = true`，否则启动明确报错，不能直接写入缺少 workload 归属的记录。保留现有 `--disable-storage` 调试方式：显式关闭存储时不要求开启上报。此约束不改变现有 Kata Guest 的存储配置行为。

### Guest 操作触发与结果上报

本版 Guest tracing/profiling 由 Guest 本地接口或已有自动触发机制启动，结果通过 GuestBridge 上报 Host。复用现有 Guest 到 Host 的文档传输，不新增 Host 向 Guest 下发采集任务的控制通道。

宿主机操作接口中的 `scope: "workload"` 仅选择沙箱在宿主机上的资源，不自动将请求转发到 Guest。Guest 整机采集使用 Guest 本地整机采集能力，其执行位置和结果归属需要区分：在 Guest 内执行，结果按所属沙箱归属，不能把 Guest 本地整机请求解释为采集物理宿主机。

### Guest 上报中断与数据丢失

本版沿用现有 VSOCK 文档通道的上报语义，接受断连或队列容量不足时丢失部分事件、tracing 和 profiling 数据：

- 连接失败或中断后自动重连，恢复后继续发送新数据。
- 断连期间产生的数据、检测到断连时尚未发送的队列数据，以及队列满时无法入队的新数据会被丢弃，不在重连后补传。
- 不增加本地落盘补传或 Host 保存成功的逐条确认；Guest 入队或发送成功不代表 Host 已完成保存。
- 保留现有错误记录和丢弃统计，使上报异常可被观察。

该约定只涉及 Guest 文档传输，不改变发现失败时保留已有对象和有效资源绑定的规则。

## 公共资源绑定

### cgroup 路径与控制器

CubeSandbox 返回的路径相对于各 cgroup 挂载点。v1 下，`cpu,cpuacct`、`memory` 等挂载点分别存在同一相对路径；v2 下使用统一层级。

| 模式 | 绑定 | 实际位置示例 |
| --- | --- | --- |
| v1 | `cpu` 与 `cpuacct` | `/sys/fs/cgroup/cpu,cpuacct/cube_sandbox/sandbox/8` |
| v1 | `memory` | `/sys/fs/cgroup/memory/cube_sandbox/sandbox/8` |
| v2 | 统一层级 | `/sys/fs/cgroup/cube_sandbox/sandbox/8` |

实际挂载位置由公共层查询，不把表中的挂载目录写死在 Source 中。共用一个挂载点的控制器仍可按各自名称查询绑定。

`CgroupBinding` 表达 v2 统一路径和 v1 各控制器路径，复用 `internal/cgroups/process_paths.go` 中已有能力。公共查询按所需控制器返回绑定；一般容器的不同控制器路径可以不同，不能把 CubeSandbox 的同路径约定推广到所有来源。

来源返回的定位信息经过本机校验后才能用于采集。目录身份、绑定有效性与 CSS 信息应保持一致，相关索引随缓存提交更新。CSS 暂不可用不影响基本文件指标，单个控制器缺失也不影响其他控制器。

来源查询失败时保留上次元数据，报告发现状态异常，并继续使用通过现有校验的旧绑定采集。目录消失或已有检查确认绑定失效时，停止对应采集，等待来源恢复后确认。此次不增加用于识别目录删除后立即同路径重建的专项机制。

### 进程与网络

`Process` 表示来源明确提供的宿主机代表进程，不是所有采集的必填项。整个沙箱的进程枚举和宿主机 profiling 可以从已验证的 cgroup 获取目标，并在使用时检查 PID 启动身份，避免 PID 复用。

网络地址信息与可访问的网络资源绑定分开判断。只有 IP 时允许保存元数据，不能默认使用 shim 或宿主机网络命名空间。没有可靠关联时跳过对应网络采集。

缺失路径、进程或网络绑定都不能隐式退回宿主机根 cgroup、PID 1 或默认网络命名空间。宿主机采集必须是明确选择的独立路径。

## 缓存与实例识别

单来源缓存按 ID 索引；`Pod.Type` 保留来源类别，用于对象描述和对外输出。复用现有内部实例代次，区分同一 ID 的不同运行实例；代次不要求跨 agent 重启持久化。

| 变化 | 处理 |
| --- | --- |
| 名称、标签等元数据变化 | 更新视图，保留实例及采集状态 |
| 首次补齐原来未知的资源或启动信息 | 不应仅因字段从空变为有值就判断重启 |
| 已确认的运行实例被替换 | 更新实例代次，重建全部实例采集状态 |
| 来源查询失败 | 保留成员和元数据；不把错误当作退出 |
| 完整列表确认对象缺失 | 移除对象并通知订阅者 |

Container 沿用已有实例识别行为。CubeSandbox 使用沙箱 ID 标识对象，不使用 cgroup 目录编号作为 ID；已确认 `StartedAt` 在恢复和回滚时可能保留，不能据此保证识别每次 VM 重建。本版不增加相应的专项资源替换识别机制。

来源正常同步时，旧沙箱被删除、新沙箱被发现，分别按删除和创建处理，新对象获得新的采集状态。同一对象的资源替换，以及来源失联期间的目录编号复用，不列为本版需要新增处理的场景。

## 同步调度与生命周期通知

Manager 为选定来源维护初始化、同步状态、超时和重试，复用一条同步循环。全量同步串行执行，避免旧响应覆盖新状态。网络与文件读取在 Store 锁外完成，提交时统一更新视图与关联索引。

定期全量同步保证最终收敛；cgroup 事件只加速核对，不替代来源查询。未知资源或事件丢失可以触发合并后的全量刷新，避免每个内核事件都发起外部请求。

生命周期事件触发刷新后，即使查询成功，也在 1 秒、2 秒的间隔后各补一次查询，避免内核事件先于 kubelet 等来源的状态更新而错过新对象。该规则同样适用于无法归属到对象的事件；没有后续事件且查询成功时，三次查询后恢复正常同步周期，不因无关 cgroup 持续重试。查询失败仍按已有退避策略处理。

通知复用现有有界队列和溢出后全量重同步机制。首次快照与后续增量提交应保持一致，不能在订阅建立过程中漏事件。查询失败时保留已有视图，错误不代表空列表；恢复后的完整快照用于重新核对成员。

对象创建、删除和已识别的实例替换沿用现有通知方式；实例替换按旧实例删除、新实例创建的顺序处理。资源首次就绪时应唤醒相应订阅者，具体复用现有通知的方式在接口细化时确定，不新增独立绑定代次。

公共事件归属依据已验证资源与实例的关联，不从 CubeSandbox 的数字目录名提取 ID。cgroup 删除事件可以立即使相关绑定失效，但对象是否退出仍由完整发现结果确认。

保留 Container 来源内部已有的 Kata 代表对象选择行为。由于每次只启用一个来源，不增加跨来源资源所有权或去重规则。

## 采集状态与调用方迁移

`lifeResources` 保存 CPU 差值基线等实例内状态。元数据更新保留状态，已识别的实例替换重建全部状态。沿用现有采集器的状态管理，不引入资源依赖框架或按资源分别重置的机制；现有文件描述符和后台任务的释放行为保持不变。

除 `internal/pod` 外，需要检查以下调用方：

| 调用方 | 迁移重点 |
| --- | --- |
| `core/metrics` | 使用公共资源绑定，迁移 `Container` 参数及生命周期缓存，保持容器筛选行为 |
| `core/events` 与 `core/autotracing` | 使用公共事件归属、实例引用和绑定校验 |
| profiling 入口 | 使用明确的对象或 cgroup 目标，不再必须通过容器 ID 查询运行时 |
| `pkg/metric`、`internal/document` | 定义 workload 身份及容器专有字段的输出规则 |
| API、matcher 与配置 | 保留原有容器语义，显式接入沙箱；不把十六进制容器 ID 校验用于所有来源 |
| `internal/guestbridge` 与 Guest 扫描 | 保持已有身份关联与 Host/Guest 数据边界 |

内部统一使用 `Pod`，现有容器指标名称、标签和 API 契约保持兼容。沙箱对外统一使用 `workload_id` 和 `workload_type`，事件、存储文档、查询与 profiling 请求使用同一组字段名称；不为 CubeSandbox 和 OpenSandbox 各建一套身份字段，也不把沙箱伪装成普通容器写入旧字段。

```json
{
  "workload_id": "42216e9f562a484fa1214c784b95e65c",
  "workload_type": "cubesandbox"
}
```

适配集中在输出和 API 边界，内部不维护两套管理逻辑。对外使用 workload 避免与 Kubernetes Pod 混淆；内部保留已确定的 `Pod` 名称。

指标输出中，现有容器继续使用 `container_` 前缀，沙箱统一使用对应的 `workload_` 前缀；保留采集器原有的命名空间、子系统和指标含义。CubeSandbox 与后续 OpenSandbox 共用指标名称，通过 `workload_type` 区分，不为各来源分别增加指标命名体系。底层复用公共采集器，前缀与身份标签转换在输出边界完成。

### 沙箱指标标签

沙箱指标固定携带以下公共标签，现有容器标签保持原样：

| 标签 | 值与缺省规则 |
| --- | --- |
| `host`、`region` | 沿用现有宿主机信息 |
| `workload_id` | `Pod.ID` |
| `workload_type` | `Pod.Type` |
| `workload_name` | `Pod.Name`，名称为空时使用 ID |
| `workload_namespace` | 来源提供的命名空间，未知时保留该标签并使用空字符串 |

保留采集器自身的 CPU、设备等标签，不自动将 `Pod.Labels` 全量转换为指标标签，避免标签集合随来源数据变化。不为沙箱补充容器专有标签。

### 过滤配置的兼容与后续统一

2026-11-07 确认：沙箱的 `memory.stat` 字段过滤先复用 `MetricCollector.Vmstat.IncludedOnContainer` 和 `ExcludedOnContainer`。这两个字段作用于容器和沙箱的原始 `memory.stat` 字段名；宿主机 `/proc/vmstat` 继续独立使用 `IncludedOnHost` / `ExcludedOnHost`。本版保留旧字段名，不新增一套沙箱专用过滤配置。

**后续统一事项（未实施）**：统一面向 workload 的过滤配置命名与作用范围，首先处理 vmstat 中带 `OnContainer` 的兼容名称。统一名称、旧配置迁移方式、同时提供新旧字段时的规则及弃用安排另行确定；保留旧配置行为并补充兼容测试后再切换。本次复用不视为这项统一工作已完成，也不在当前迁移中删除旧字段。

### 事件、tracing 与 profiling 存储映射

沙箱记录使用顶层字段 `workload_id`、`workload_type`、`workload_name` 和 `workload_namespace`，名称和命名空间沿用前述取值规则，不填写 `container_*` 字段。现有容器记录的字段与含义保持原样。

`source_type` 表示数据在哪里采集，`workload_type` 表示采集对象的来源类别，两者不能混用。本版先保持兼容，来源标记按以下规则输出：

| 采集位置与对象 | `source_type` | `workload_type` |
| --- | --- | --- |
| 宿主机侧 CubeSandbox 资源 | `host` | `cubesandbox` |
| CubeSandbox Guest | `guest` | `cubesandbox` |
| 现有 Kata Guest | 保留 `kata_guest` | 保持现有容器契约，不强行补充 workload 字段 |

后续其他沙箱 Guest 复用 `source_type: "guest"`，通过 `workload_type` 区分来源，不为各类沙箱增加专属 Guest 来源值。现有 Kata 的 `kata_guest` 本版继续写入并兼容读取，历史记录也不重写；后续再单独设计统一迁移，不能在本次重构中静默改名。相关文档校验、API 描述和存储映射需接受新增的 `guest` 值，并保持对旧值的兼容。

沿用现有存储后端和文档结构，补充公共 workload 字段及相应映射，不为各类沙箱另建一套表或索引体系。

### 沙箱元数据查询

增加 `GET /v1/workloads/{workload_id}` 查询沙箱元数据，直接读取 Manager 缓存，不因查询请求临时访问 cubelet。每个 agent 实例只有一个来源，按 ID 查询即可，不要求请求额外携带来源类型。

响应沿用现有 `data` 外层结构，返回 `workload_id`、`workload_type`、`workload_name`、`workload_namespace` 和 `cgroup_css`。名称与命名空间沿用上述指标标签的取值规则，CSS 沿用现有容器接口的控制器名称到地址字符串的映射格式，仅返回当前已绑定的有效信息。对象已发现但 CSS 尚未绑定时仍返回对象元数据，`cgroup_css` 为 `{}`；CSS 为空不等于对象不存在，也不代表基本 cgroup 文件指标不可采集。

现有 `GET /v1/containers/{container_id}` 保持原契约。本版不增加 workload 列表接口，也不直接序列化整个内部 `Pod` 作为外部响应。

### 操作请求与目标校验

沿用现有操作接口，新增 `scope: "workload"` 指定沙箱目标，不单独增加 CubeSandbox 或 OpenSandbox 的 profiling 操作接口。已有 `host`、`container` 请求格式保持兼容。以下示例仅展示请求的目标部分，其他必填操作参数保持现有约定。

容器目标继续使用：

```json
{
  "scope": "container",
  "container_id": "..."
}
```

沙箱目标使用：

```json
{
  "scope": "workload",
  "workload_id": "42216e9f562a484fa1214c784b95e65c",
  "workload_type": "cubesandbox"
}
```

| scope | 身份字段约定 |
| --- | --- |
| `host` | 不携带容器或 workload 身份字段 |
| `container` | 必须提供 `container_id`，不携带 workload 身份字段 |
| `workload` | 必须提供 `workload_id` 和 `workload_type`，不携带 `container_id` |

混用两套身份字段时直接报错，不设置隐含优先级。校验通过后在 API 边界转换为内部 `Pod` 目标，再检查对象是否存在、类型是否匹配及是否具备所请求的采集能力。仅有宿主机 cgroup 的沙箱不能因此宣称支持 Guest 应用 profiling。

本版宿主机 `scope: "workload"` 操作不包含 Guest 远程执行。Guest tracing/profiling 由 Guest 本地触发并上报结果，具体边界见 Guest 接入章节。

## 实施顺序与验证

1. 明确容器兼容适配与新增 workload 身份字段，引入公共对象、来源接口和 Store 分工，将现有 Container 发现适配到新接口。
2. 拆出公共 cgroup 绑定、实例校验和通知，迁移采集器的资源访问，保留 Kata 与 Guest 场景。
3. 升级 Go 构建环境与 CI，固定 CubeSandbox protobuf 依赖，接入全量查询，验证 v1/v2 绑定及仅有 cgroup 时的采集；完成 CubeSandbox Guest 事件、tracing 和 profiling 的采集、文档传输与 Host 身份关联，验证端到端链路。
4. 完成单来源配置、API、指标和存储输出适配，保持容器对外契约，移除已无调用方的内部旧接口。
5. OpenSandbox 协议确定后实现来源，公共采集器不增加来源专属分支。

每一步都保持可编译、可验证，不一次性进行无关重构。单元测试扩展已有测试文件；cubelet、挂载与内核交互通过集成测试验证。

| 验证场景 | 成功条件 |
| --- | --- |
| Container 迁移 | 现有容器、Kata、Guest 行为及选择规则不回退 |
| 单来源启动 | 只创建选定来源，不查询其他来源 |
| 统一禁用发现 | `--disable-workload-discovery` 和旧别名 `--disable-kubelet` 均关闭所选来源或 Guest 本地扫描，不启动 Manager；宿主机采集继续 |
| GuestBridge 与禁用发现冲突 | 开启 GuestBridge 且关闭 workload 发现时启动明确报错 |
| CubeSandbox Guest 接入 | 在发现启用时支持 GuestBridge；Guest 事件、tracing 及 profiling 文档按整机粒度关联到正确沙箱并保留进程信息，Host 与 Guest 数据不混淆；不要求发现内部应用容器 |
| CubeSandbox Guest 指标范围 | 本版不增加 Guest 指标输出和传输，宿主机侧沙箱 cgroup 指标继续采集 |
| CubeSandbox Guest 操作触发 | Guest 本地触发 tracing/profiling 并通过 GuestBridge 上报；宿主机 `scope: "workload"` 只采集宿主机资源，不向 Guest 下发任务 |
| Guest 来源标记兼容 | CubeSandbox Guest 使用 `source_type: "guest"` 与 `workload_type: "cubesandbox"`；Host 数据继续使用 `host`，现有 Kata Guest 保留 `kata_guest`，旧记录可读且不重写 |
| CubeSandbox Guest 身份 | 无需注入沙箱身份或现有 Kubernetes 身份环境变量即可启动采集与上报；Host 核验连接后从 Manager 补齐 workload 归属，未知归属连接的数据不接收，现有 Kata 配置要求保持兼容 |
| CubeSandbox Guest 启动配置 | `Mode = "sandbox"`、`Pod.Source = "cubesandbox"`、`GuestTransport.Enabled = true` 启用 Guest 整机采集及 VSOCK 上报，不连接 cubelet、不启动内部应用发现；默认 Container 来源下的 Kata Guest 行为保持兼容 |
| CubeSandbox Guest 存储配置 | 正常启用存储却未启用 `GuestTransport` 时启动报错，不直接写入无 workload 归属的记录；`--disable-storage` 调试方式仍可使用，Kata Guest 原有配置保持兼容 |
| CubeSandbox Host 部署与连接核验 | 宿主机进程或 `hostPID` 容器能访问 cubelet socket 和沙箱运行目录；按监听路径确定候选沙箱，`SO_PEERCRED` 对端 PID 与其 shim PID 匹配才接收数据；错误 PID 或未知归属连接被拒绝 |
| Guest 上报中断 | 自动重连；断连期间产生的数据、检测到断连时未发送的队列数据及队列满时无法入队的数据按现有语义丢弃并记录，不补传，不将入队或发送成功视为 Host 保存成功 |
| 全量同步周期 | 默认 30 秒，配置调整后按设定周期发现对象，不改变指标采样周期 |
| 来源与 socket 配置生效 | 修改配置文件并重启后生效；运行时接口拒绝实际变更，配置和活动来源保持原状 |
| cubelet 超时或返回损坏数据 | 不误删沙箱，报告发现异常，通过现有校验的旧绑定继续采集 |
| List 原始返回数量达到 5000 | 状态筛选前标记不完整并报告错误，更新有效记录，不根据缺失项删除旧对象 |
| 完整空列表及部分未解析记录 | 正确删除缺失对象，保留已知但未解析的成员 |
| 未返回 cgroup 路径或目录尚未创建 | 保留对象，不采集宿主机资源，后续能恢复绑定 |
| v1 与 v2 | 各控制器读取正确，挂载位置不依赖硬编码 |
| 正常发现对象创建与退出 | 创建新状态、释放旧状态，保留现有实例校验行为 |
| 暂停、退出与恢复运行 | 只采集运行态，完整同步后停止非运行态采集，恢复运行后重新加入 |
| 仅有 shim PID 或 IP | 不产生虚假的进程、网络采集能力 |
| 订阅溢出、来源故障恢复、资源首次就绪 | 全量恢复不漏有效对象，查询错误不触发清空 |
| Kata 共享资源 | 保持已有代表对象选择行为 |
| 对外兼容 | 容器指标与 API 保持原契约；沙箱指标使用 `workload_` 前缀与明确的 workload 身份，通过 `workload_type` 区分来源 |
| 沙箱指标标签 | 固定携带 `host`、`region`、`workload_id`、`workload_type`、`workload_name`、`workload_namespace`；名称为空时使用 ID，未知命名空间输出空字符串；保留采集器自身标签，不自动展开 `Pod.Labels` |
| 沙箱元数据查询 | 新接口从 Manager 缓存按 ID 返回身份、名称、命名空间和已绑定的 CSS，不临时调用 cubelet；已发现但 CSS 未绑定时返回元数据与空 CSS 集合；旧容器接口保持兼容 |
| 沙箱存储映射 | 事件、tracing 和 profiling 使用顶层 workload 身份、名称和命名空间字段，不填写 `container_*`；宿主机采集记录保持 `source_type: "host"`，容器记录契约保持兼容，复用现有存储体系 |
| 操作请求目标 | 旧 host/container 请求继续可用；workload 请求使用完整的 workload 身份，混用身份或缺少必要字段时明确报错 |
| Go 构建版本与 protobuf 依赖 | 使用升级后的工具链完成必要构建与检查，依赖版本固定且可获取，不依赖本地 CubeSandbox checkout |

涉及 BPF 事件处理与指标循环的修改，使用已有 benchmark 比较开销；验证不在热路径增加外部查询或无必要的分配。按改动范围运行单元和集成测试，提交前执行 `make check`。本次仅编写方案，不执行代码迁移。

## 实施接口细化

以下定义供开发计划步骤 01 起逐步实现，不表示当前代码已经具备这些接口。实现与验证进度另见[开发计划](/docs/development/workload_manager_development_plan_zh.md)。

### 公共资源字段与查询

```go
type CgroupBinding struct {
	Unified     string            `json:"unified,omitempty"`
	Controllers map[string]string `json:"controllers,omitempty"`
	CSS         map[string]uint64 `json:"css,omitempty"`
}

type HostProcess struct {
	PID int `json:"pid"`
}

type HostNetwork struct {
	PID             int    `json:"pid,omitempty"`
	NamespaceInum   uint64 `json:"namespace_inum,omitempty"`
	NamespaceCookie uint64 `json:"namespace_cookie,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
}

type ContainerInfo struct {
	Hostname    string        `json:"hostname"`
	Type        ContainerType `json:"type"`
	Qos         ContainerQos  `json:"qos"`
	RuntimeType string        `json:"runtime_type,omitempty"`
	SandboxID   string        `json:"sandbox_id,omitempty"`
}
```

`Unified`、`Controllers` 使用相对于 cgroup 挂载点的成员路径，沿用 `cgroups.ProcessPaths` 的表达方式，例如 `/cube_sandbox/sandbox/8`；不是带 `/sys/fs/cgroup` 的绝对文件路径。v1 的控制器分别查询，v2 复用 unified 路径，混合模式优先明确的控制器成员路径。公共绑定层解析实际挂载位置，采集器不能自行拼接固定挂载目录。

Source 填写待校验位置，不填写 CSS。发布给采集器的路径和 CSS 必须经过公共绑定层核验；尚未就绪的位置保留在内部待绑定记录中，不作为可采集路径返回。公共查询按控制器返回路径和可用性，缺失返回不可用，不能用空路径表达宿主机目标。进程枚举另有明确查询，复用现有 unified/cpu/cpuacct/pids 选择规则。

`HostProcess.PID` 为采集代表进程；进程启动身份保留在 Store 的内部记录并在使用时核验，不由 API JSON 暴露。`HostNetwork.PID` 仅用于访问已确认的网络命名空间，需验证对应进程和 netns；仅有 IP 不表示网络资源可用。CubeSandbox 初始 `Process`、`Network` 均为 nil，IP 保存为来源元数据，命名空间使用公共 label key `workload_namespace`。

公共查询新增 `PodByID` 和 `Pods`，返回已发布只读视图；返回 nil 对象表示缓存中没有可用对象。发现状态通过 Manager/订阅状态独立查询，不能因来源暂时失败而隐藏已有元数据。旧 `ContainerByID`、`Containers` 及容器分类入口在迁移期仅返回 `TypeContainer`。

BPF 指标使用 `PodsByCgroupCSS(controller, containerTypes)` 获取同一缓存快照下的 CSS 到 Pod 映射。`containerTypes` 仅延续 Container 来源的旧类型筛选，沙箱不要求 ContainerInfo；零 CSS 不返回。Kata 共享地址沿用兼容视图的稳定首次发现时间选择代表，不能使用每次刷新的 Pod.SyncedAt 重新排序。查询不触发来源或内核 I/O，不增加一份实例缓存。


Store 每条记录以 Pod 为公共对象。为避免一次迁移全部调用方，可在同一记录内保留一次提交生成的只读 Container 兼容视图，和 Pod 共用同一份实例采集状态；禁止另建容器 Store 或独立更新该视图。来源返回的 map 和视图在提交前建立明确所有权，提交后不可原地变更。

迁移期间，`ContainerInfo` 以不序列化字段保留旧视图所需的 `CgroupPath`、Guest 扫描标记及独立 `MemoryCgroupPath`。Guest 缺少内存控制器时，旧视图仍明确返回不可用，不回退到 CPU 路径。Manager 的 `ProcRoot` 选项接收原有 Guest 扫描根目录，使 mountinfo、进程及网络核验读取同一本地 proc 视图；默认沿用现有 proc 配置，不新增用户配置开关。Store 不保存来源实例或扫描器指针，Guest 引用校验使用已发布的进程与本机状态。容器来源由启动方通过 `container.NewSource` 创建并传入 `pod.StartManager`；根包不反向依赖来源子包。容器专有的本地 runtime 查询和引用校验留在来源子包，公共层提供目录身份/CSS 与内核资源校验，旧 CLI/API 的行为由调用方适配保持。

现有 Kata `keepKataRepresentative` 会用容器 `SyncedAt` 保持首次发现成员优先，而公共 `Pod.SyncedAt` 表示最近成功提交时间。迁移时保留内部首次发现时间和旧容器视图的时间语义，Kata 代表排序继续使用原稳定时间；不能因普通同步重新选代表。旧容器 JSON 字段、类型及 QoS 编码不变。

### 首次资源就绪通知

资源由未知变为首次可用时，保留实例 Key、Generation 和 lifeResources，使相关订阅进入已有的 `needsFull` 路径并发出唤醒。订阅者按全量视图重新核对同一实例的资源，不新增事件种类或绑定代次，也不发出虚假的删除/创建。

现有内存 watcher 对相同 Generation 会直接跳过。迁移该调用方时必须同步允许同一实例从“没有绑定/没有注册”进入首次绑定；已经有效注册的对象仍保持原任务、基线和冷却状态。该行为需要 Store 通知与调用方的联合测试，不能只验证收到 wakeup。

### Guest 连接核验信息

保留 Pod 的公共采集资源结构，在 `Discovery` 中增加可选的 `GuestPeers map[string]GuestPeer`，键为同次发现的对象 ID。第一版 `GuestPeer` 仅包含 `PID int`，用于 CubeSandbox 的 shim 对端核验，不扩展通用传输注册框架。

Manager 将这份信息与对应 Pod 在同一 Store 提交，并提供按 ID 的只读查询。有效 Pod 未提供有效 peer 信息时，清除该记录的连接核验能力；nil 成员或发现失败保留旧记录的核验信息，对象删除时一同删除。没有对应有效 Pod 的 peer 信息不得单独创建身份。连接建立时仍须完成已约定的 Unix 对端 PID 核验。

GuestBridge 从 Manager 获取身份与 peer 信息，不反向调用来源；监听目录根据配置和沙箱 ID 确定。Kata 继续使用既有 runtime/VM 关联，不要求所有 Container 来源都填写 `GuestPeers`。首次引入此字段时更新 `Discovery` 代码定义、构造和验证用例。

### 配置字段

| 字段 | 默认值 | 生效与校验 |
| --- | --- | --- |
| `Pod.Source` | `container` | 启动期；必须为已实现且适用于当前 Mode 的来源 |
| `Pod.SyncIntervalSeconds` | `30` | 正整数；启动时传给 Host Manager；不改变指标周期或现有 Kata Guest 的 5 秒扫描周期 |
| `Pod.DiscoveryTimeoutSeconds` | `10` | 正整数；每次 Host 发现的整体超时，重试仍由 Manager 控制 |
| `Pod.CubeletSocket` | `/data/cubelet/cubelet.sock` | 启动期；CubeSandbox Host 使用规范的绝对 Unix socket 文件路径 |
| `GuestBridge.CubeSandboxRoot` | `/run/vc/vm` | 启动期；规范的绝对目录路径，不能是 `/`；遵循原 GuestBridge 配置更新限制 |

同步周期与发现超时在本版也仅启动读取，实际运行时变更拒绝并要求重启，避免配置展示值与活动调度不一致。对未使用来源不创建客户端。`guest_scanner.go` 暂留根包作为既有 Kata Guest 本地发现适配器，不为目录形式新增无意义子包。

真实 Unix peer PID、运行目录挂载、v1/v2 和 Guest 数据链路仍须在对应开发步骤做集成验证；接口细化完成不代表这些场景已验证。

---
title: Pod 多来源重构开发计划
type: docs
description: 按可独立编译、验证的功能增量，迁移公共管理层并接入 CubeSandbox Host 与 Guest。
author: HUATUO Team
date: 2026-11-05
---

# Pod 多来源重构开发计划

本计划以 [Pod 多来源重构方案](/docs/development/workload_manager_design_zh.md) 为依据。先保持现有 Container 和 Kata 行为完成公共层迁移，再接入 CubeSandbox Host，最后打通 Guest 数据链路。本文只规划开发顺序，不表示任何步骤已经实现或通过验证。

## 开发与提交原则

1. 每次只交付一个功能或一个明确的内部迁移点，同时完成它的调用方适配、必要测试和文档。不能提交需要下一次修改才能编译的中间状态。
2. 默认配置始终保留现有 Container 行为。新能力完成到哪一步，就验证到哪一步；未完成的运行组合明确拒绝，不能启动后静默走错来源。
3. 内部最终统一使用 `Pod`。迁移期间允许旧查询和参数入口转接公共实现，但只保留一个 Store、一套实例状态和一条同步循环，不双写两套缓存。
4. 旧容器入口只返回 Container 对象。尚未迁移的调用方不能拿到沙箱并将其写成容器数据，也不能在目标缺失时退回宿主机资源。
5. 涉及一组采集器的步骤，按具体采集器分别提交。每次包含该采集器从资源读取到输出的完整迁移，不做一次性全仓库类型替换。
6. 单元测试扩展已有测试文件，不新建单元测试文件。包拆分时同步迁移相应的现有测试；cubelet、挂载、Unix socket、VSOCK 等外部交互增加集成测试。
7. OpenAPI、mock、Cap'n Proto 等生成产物通过 `make gen-build` 更新，不手工修改。Go 文件遵循仓库许可证规范。

不增加多来源并行管理、资源替换专项框架、OpenSandbox 占位实现、CubeSandbox Guest 指标、Host 向 Guest 下发任务或断连补传。这些均不属于本次开发范围。

## 每次改动的完成条件

下面各步骤的专属用例，是以下公共检查之外的要求。

| 检查 | 要求 |
| --- | --- |
| 编译 | 在项目支持的 Linux 构建环境执行 `make build`，包含受影响的 agent、apiserver、生成代码及 BPF 产物，不能只编译新包 |
| 行为 | 运行受影响包的现有单元测试及本次补充用例；涉及外部依赖时运行对应集成用例 |
| 兼容 | 运行该改动涉及的 Container、Kata Host/Guest 回归用例，确认旧输出和选择规则未变 |
| 提交检查 | 提交前执行 `make check`；检查其生成、依赖整理及格式化是否产生了额外差异 |
| 并发与性能 | 修改 Store、订阅或连接生命周期时运行适用的 race 测试；修改 BPF 事件处理或指标循环时提供同环境修改前后的 benchmark 结果 |
| 证据 | 记录命令、环境、结果及未执行项；缺少内核能力、cubelet 或真实 VM 导致的 SKIP 不算该能力验收通过 |

`make check` 会整理 vendor、生成文件、格式化，并执行 `git diff --exit-code`。开发时在隔离的工作副本准备好本次候选变更再运行检查，避免把已有无关修改混入结果。发现检查新产生的改动，审查并纳入本次必要变更后重新验证，不修改检查规则绕过失败。

常用验证入口：

```bash
make build
go test -count=1 ./internal/pod/...
go test -race -count=1 ./internal/pod/...
make check
```

按实际改动替换测试包。集成测试先完成构建，再用 `bash integration/run.sh <用例文件名>` 选择用例；命令格式和环境要求见[集成测试文档](/docs/development/integration_zh.md)。阶段验收再运行 `make unit`、适用的 `make integration` 和 `make e2e`，无需在每次纯内部小改动后重复全部端到端测试。

## 顺序与阶段交付

按下表顺序推进。依赖列列出直接依赖；同一编号带有“逐项提交”的，必须为每个实际采集器建立单独任务，不把整行当成一次提交。

| 步骤 | 单次交付目标 | 直接依赖 |
| --- | --- | --- |
| 00 | 固定接口细节、回归清单和验证基线 | 无 |
| 01 | Store 内部改用 `Pod`，保留容器兼容入口 | 00 |
| 02 | 公共 cgroup、进程和网络绑定 | 01 |
| 03 | 公共订阅及资源首次就绪通知 | 02 |
| 04 | Container 发现接入统一 Source 和 Manager | 03 |
| 05 | 显式来源配置和统一禁用发现开关 | 04 |
| 06 | 公共 CPU 指标及 workload 指标输出 | 05 |
| 07 | 公共内存指标 | 06 |
| 08 | 其余资源采集器逐项迁移、逐项提交 | 07 |
| 09 | 升级 Go 工具链和构建环境 | 08 |
| 10 | CubeSandbox Host 发现与 cgroup 指标链路 | 09 |
| 11 | workload 元数据查询接口 | 10 |
| 12 | 存储文档支持 workload 身份 | 11 |
| 13 | 事件归属逐项迁移、逐项提交 | 12 |
| 14 | profiling 支持 workload 目标 | 13 |
| 15 | tracing 支持 workload 目标 | 14 |
| 16 | autotracing 逐项迁移、逐项提交 | 15 |
| 17 | CubeSandbox GuestBridge 监听和连接核验 | 16 |
| 18 | Guest 文档归属与 Host 保存 | 17 |
| 19 | CubeSandbox Guest 启动及整机采集接入 | 18 |
| 20 | Guest 采集兼容问题逐项补齐、逐项提交 | 19 |
| 21 | 删除不再使用的内部兼容入口 | 20 |
| 22 | 部署说明与完整场景验收 | 21 |

阶段交付边界：

- **步骤 05 完成**：容器和 Kata 已使用公共管理层，默认对外行为保持兼容，CubeSandbox 尚不可启用。
- **步骤 11 完成**：CubeSandbox Host 可发现运行中的沙箱，采集可用的宿主机 cgroup 指标并查询元数据；Guest 接入尚未开放。
- **步骤 16 完成**：Host 侧事件、tracing、profiling 和适用的自动采集已接入 workload 身份。
- **步骤 20 完成**：CubeSandbox Guest 事件、tracing、profiling 可经 Host 保存，且不产生本版排除的 Guest 指标。
- **步骤 22 完成**：兼容入口清理、部署文档和真实环境验收全部完成，才视为本版本完成。

## 第一阶段：统一管理层，保留容器行为

### 00：固定实施接口和验证基线

**本次交付**：把设计文档最后列出的接口细化落到文档，形成可以逐步核对的清单，不修改实现。

- 确定 `CgroupBinding`、`HostProcess`、`HostNetwork`、`ContainerInfo` 的字段和查询方法；列清旧 `Container` 字段如何映射，尤其区分来源类型和旧容器类型。
- 确定资源首次就绪如何复用已有全量或增量通知，避免为了通知而伪造实例重启。
- 确定 Source、Manager、GuestBridge 间传递 shim 连接核验信息的最小接口；该信息不得混入代表采集进程，也不得要求 GuestBridge 再查一次 cubelet。
- 确定 cubelet socket、RPC 超时、同步周期和 CubeSandbox 运行目录的配置字段、默认值及校验规则；保持已约定的 30 秒默认同步周期。
- 从 `core/metrics`、`core/events`、`core/autotracing` 和操作入口逐个列出迁移对象，标注所需资源、是否适用于 CubeSandbox Host/Guest、对应现有测试。步骤 08、13、16、20 据此展开，不能以“剩余调用方”笼统结项。
- 保存现有 Container/Kata 的代表性指标标签、API 响应、事件和 profiling 文档，以及现有生命周期 benchmark 结果。

**验证与出口**：基线构建和适用测试有明确结果；每个未定接口都有代码依据及落点。若必须改变已确认行为，先讨论该变化；字段命名等不改变行为的细节直接落实。

### 01：Store 内部统一使用 Pod

**本次交付**：缓存以 `Pod` 保存监控对象，容器调用方仍能工作。

**主要位置**：`internal/pod/workload.go`、`store.go`、`resources.go`，现有 `container.go`、`container_events.go`、`container_life_resources.go`。

- 将容器专有元数据放入 `ContainerInfo`，建立公共身份及资源字段。
- 复用既有 Store、实例代次和采集状态，迁移存储对象；旧容器入口仅作为同一份状态的适配视图，不保留第二个可写对象缓存。
- 元数据更新发布新视图，保持实例状态；来源对象和公开视图不可被后台原地修改。
- 兼容适配不能把旧 `Container.Type` 与 `Pod.Type` 混为一谈，不使用含义不同字段的简单类型别名替代转换。

**验证**：扩展现有 `container_test.go`、`container_json_test.go`、`container_events_test.go` 和并发测试。验证元数据更新不丢 CPU 基线、已识别的实例替换重置状态、删除释放资源、旧容器序列化与查询结果保持原样。

**完成后**：仍由现有容器发现驱动，内部缓存已统一；没有需要下一步才能修复的旧调用方。

### 02：提取公共资源绑定

**本次交付**：不依赖容器运行时，也能依据来源提供的位置绑定资源。

**主要位置**：`internal/pod/cgroup.go`，现有 cgroup 路径与 CSS 代码、`internal/cgroups/process_paths.go`。

- 来源提供定位信息，公共层负责本机验证、控制器路径、CSS、进程及网络关联。容器命名和 runtime 规则暂保留在容器实现，随后随来源迁移。
- 同时支持 v1 各控制器路径和 v2 统一路径；动态识别挂载点，保留一般容器各控制器路径不同的情况。
- 支持仅有 cgroup 的对象；缺失 PID、netns、CSS 或单个控制器不会阻断其他可用资源。
- 保留已有进程与目录身份校验，不新增资源替换专项机制。

**验证**：扩展现有路径、CSS、Kata 集成用例；覆盖 v1 分挂载、合并挂载、v2、非默认挂载位置、目录延迟出现及消失。验证缺少资源时不读宿主机根 cgroup，不以 IP 或 shim PID 构造网络绑定；CSS 未就绪时文件指标仍可读取。

**完成后**：容器实际路径已使用公共绑定逻辑，合成的 cgroup-only 对象可通过集成测试验证。

### 03：统一生命周期通知和资源首次就绪处理

**本次交付**：公共对象的订阅者能收到创建、删除、已确认实例变化及首次资源就绪信息。

**主要位置**：`subscription.go`、`store.go`、`cgroup_lifecycle.go`，旧订阅入口。

- 复用有界队列、首次快照、溢出后全量重同步及原有实例代次；旧容器订阅转接同一通知实现。
- 将发现异常与已缓存对象的可读性分开，避免一次来源错误使订阅者清空或停止使用仍有效的资源。
- 按已验证的资源索引归属 cgroup 事件，数字目录不再依赖容器 ID 正则；未知事件只触发合并刷新提示。
- 资源首次补齐唤醒订阅者并保持实例状态。删除事件可使绑定失效，但不直接认定对象退出。

**验证**：现有事件、race、benchmark 用例覆盖订阅与提交交错、溢出恢复、首次就绪、错误期间有效绑定可用、失效绑定停止、关闭无泄漏。首次就绪后原实例基线不重置，同一次有效变化不造成重复后台任务。

### 04：Container 来源接入统一 Manager

**本次交付**：现有容器发现真正通过 `Source.Discover` 驱动公共管理层。

**主要位置**：`source.go`、`manager.go`、`internal/pod/container/`、Guest 扫描及 `cmd/huatuo-bamai/pod.go`。

- 将 kubelet、Docker/containerd、容器 cgroup 定位和元数据解析移入来源子包；来源子包依赖根包，启动入口注入来源，根包不反向导入来源。
- Manager 负责唯一串行同步循环、超时、周期、错误重试及停止清理；Source 不自行启动第二套调度。
- 落实 `Discovery.Pods` 与 `Complete`，区分完整空列表、nil 成员、不完整结果与查询错误，验证 ID 和来源类型一致。
- 现有 Kata Guest 本地扫描接入同一管理约定，保留其发现粒度和原有身份要求；保持 Container 的过滤规则和 Kata 共享资源代表选择。
- 搬迁代码时同步处理依赖和现有测试，移除已替代的旧同步循环。

**验证**：现有 controller、kubelet sync、runtime、Kata 和 Guest scanner 用例；新增必要的来源边界集成用例。检查超时、取消、重复/冲突 ID、nil 成员、完整空集、不完整列表及故障恢复。用慢响应验证不存在并行同步和旧响应覆盖。

### 05：显式来源配置和统一禁用开关

**本次交付**：用一个启动入口确定是否发现、使用什么来源。

**主要位置**：`cmd/huatuo-bamai/config`、CLI、`pod.go`、配置更新 handler、示例配置及配置文档。

- 增加 `Pod.Source`，默认 `container`；此时仅开放已实现的 Container Host 和 Kata Guest，`cubesandbox`、`opensandbox` 及未知值明确报错。
- 增加 `--disable-workload-discovery`，旧 `--disable-kubelet` 为废弃别名；两者均关闭 Manager 和 Guest 本地扫描，宿主机采集继续。
- 开启 GuestBridge 且禁用发现时启动报错。
- 落实同步周期配置，运行时接口拒绝修改来源。后续 socket 配置在引入当次添加同样的启动期限制。

**验证**：扩展现有 CLI、config、handler、sandbox 测试，覆盖默认行为、旧别名、Host/Guest 禁用、冲突、未知来源及配置更新拒绝后内存和持久化内容不变。

## 第二阶段：CubeSandbox Host 指标和查询

### 06：CPU 指标迁移并建立 workload 输出边界

**本次交付**：CPU 公共采集器可处理 cgroup-only `Pod`，输出由对象类别决定。

**主要位置**：`core/metrics` 中 CPU 采集器、`pkg/metric/data.go`。

- 读取公共 cgroup 绑定和实例状态，不以 `Container != nil` 或代表 PID 为 CPU 文件采集前提。
- 在指标输出边界保留容器原名称和标签，为沙箱输出对应 `workload_` 指标及六个固定公共标签。
- 名称为空取 ID、未知 namespace 取空字符串，保留 CPU 等采集器标签，不展开全部 Labels。

**验证**：CPU 与 metric 现有测试检查差值基线、输出名称及标签集合；用合成沙箱验证 `Process=nil` 可采集，固定标签不会因名称缺失或元数据更新而变化。比较修改前后的指标循环分配与耗时。

### 07：内存指标迁移

**本次交付**：内存采集器只依赖已验证的 memory 或 unified 绑定。

**主要位置**：内存指标采集器、对应的实例内状态访问。

**验证**：运行现有内存测试并补充 cgroup-only 场景；缺失 CPU 控制器不影响内存，memory 缺失时跳过对应指标，CSS 缺失不影响可读取的文件值；v1/v2 和 Container/Kata 的原有选择规则保持。若涉及多个独立内存采集器，逐个提交完整迁移。

### 08：其余资源采集器逐项迁移

**本次交付**：每次迁移一个采集器，依照步骤 00 的清单推进 IO、网络及其余资源访问。

- 每个任务写明所需控制器、进程或网络绑定，使用公共查询与输出。
- 有资源就采集，没有资源就跳过对应对象。CubeSandbox 只有 IP 时不得启动依赖 netns 的采集。
- 保持容器 matcher 和原有排除规则，不把容器专有筛选条件硬套给沙箱；发现需要新增用户可见筛选语义时先回到设计确认。

**验证**：每个采集器分别跑原用例、资源缺失用例及适用的 v1/v2 集成测试，检查容器输出兼容、沙箱不误采宿主机、删除时释放资源；提供涉及热路径的 benchmark。

**阶段检查**：此时仍只开放 Container；公共采集能力通过合成对象和资源集成环境验证，不创建生产环境的假来源。

### 09：升级 Go 构建环境

**本次交付**：项目使用能够引入 CubeSandbox proto 模块的工具链构建。

**主要位置**：`go.mod`、构建镜像、CI、工具安装与编译说明中实际使用的 Go 版本。

- 使用已同意的 Go 1.25.7 或兼容的更高版本；实施时固定具体版本。
- 只更新必要工具链及兼容调整，不顺带升级无关依赖。proto 模块在下一步有实际 import 时加入，避免 `go mod tidy` 删除未使用依赖。

**验证**：升级后的环境运行完整构建、`make unit`、`make check` 和现有发行构建检查，确认生成工具可运行、vendor 一致。记录与升级前基线的差异。

### 10：接入 CubeSandbox Host 来源

**本次交付**：配置 `Pod.Source="cubesandbox"` 后，可经 cubelet 发现沙箱并输出宿主机 cgroup 指标。

**主要位置**：`internal/pod/cubesandbox/{client,source}.go`、Host 启动入口、socket 配置及示例。

- 固定可获取的 `github.com/tencentcloud/CubeSandbox/pkgs/proto` 模块版本，更新 vendor；不依赖本地 checkout。
- 复用 Unix gRPC 连接，调用 `CubeboxMgr/List`，不传 ID，开启 `private_with_cubebox_store`，直接解码私有 bytes 中的 JSON。
- 用外层 ID 保留未解析成员，用代表沙箱的容器状态选择运行态；StartedAt 按整数纳秒读取，不从 JSON 时间戳猜测状态。
- 原始 items 数量达到 5000 时先标记不完整和错误，再做状态过滤；保留有效更新但不按缺失删除。
- 提供 cgroup 相对路径和连接核验需要的 shim 信息，保持 `Process=nil`，IP 不生成 Network 绑定。
- 加入 socket 地址启动期限制，查询失败不切换来源。此步开放 CubeSandbox Host；尚未完成的 CubeSandbox Guest 及其 GuestBridge 组合仍明确拒绝。

**验证**：使用 Unix gRPC 测试服务做协议集成测试，覆盖请求、超时、损坏详情、缺失 ID、冲突 ID、5000 条截断、完整空集及暂停/恢复；再用真实 cubelet 验证运行态、v1/v2 指标、名称回退、发现故障保留有效采集、恢复收敛及进程退出后的连接清理。

### 11：新增 workload 元数据查询

**本次交付**：`GET /v1/workloads/{workload_id}` 可查询已发现沙箱。

**主要位置**：node OpenAPI、生成的客户端和服务端类型、handler 与路由。

- 从 Manager 缓存返回约定的 `data`、workload 身份、名称、namespace 和 `cgroup_css`。
- 未绑定 CSS 时返回 `{}`，不存在时使用现有错误约定；不直接序列化 Pod，不新增列表接口。
- workload ID 不套用容器 64 位十六进制规则；旧容器接口保留原契约。

**验证**：扩展现有 node API 和 container handler 测试，并通过真实 agent 请求验证已发现对象、CSS 空/有效、未知对象。记录 cubelet 调用次数，证明 API 请求不触发外部查询；重新生成并编译所有 API 调用方。

## 第三阶段：Host 事件和操作链路

### 12：存储文档支持 workload 身份

**本次交付**：现有事件、tracing、profiling 文档结构能够表达沙箱身份。

**主要位置**：`pkg/types/document.go`、`internal/document`、现有存储映射、API 中的记录定义。

- 添加四个顶层 workload 字段及 `source_type: "guest"` 的兼容读取和校验；旧 `host`、`kata_guest` 及历史文档行为保留。
- 公共文档构造从 Pod 提取身份，沙箱不写 `container_*`，Host 沙箱数据仍标记 `host`。
- 更新实际受影响的写入、读取、查询映射与文档，不为不同来源创建新表或索引体系。
- 此步只建立存储能力，不开放未经认证的 Guest 写入路径。

**验证**：扩展 builder、文档及存储现有测试，验证容器序列化兼容、沙箱字段不混用、历史记录可读和新字段往返不丢失；用现有存储后端做必要的集成验证。

### 13：事件归属逐项迁移

**本次交付**：每次迁移一个事件采集器的资源归属与输出。

**主要位置**：`core/events`、公共 CSS/进程/网络关联查询、事件文档构造。

- 事件通过已验证的公共索引找到 Pod，并使用步骤 12 的身份输出。
- 不再从沙箱数字目录解析容器 ID；没有所需绑定时不能误归属或输出带假容器身份的记录。
- 保留现有无对象事件的明确宿主机语义，区分“本来就是宿主机事件”和“沙箱目标已失效”。

**验证**：每个事件用例检查实际资源、归属和原始文档；至少覆盖正常、未绑定、删除后迟到事件、来源短暂故障，以及 Kata 共享资源代表规则。修改 BPF 事件热路径时附性能比较。

### 14：profiling 支持 workload 目标

**本次交付**：现有 profiling 接口可以选择沙箱宿主机资源并保存带 workload 身份的结果。

**主要位置**：`pkg/observation`、OpenAPI、apiserver 转发、nodeagent profiling 的校验与执行、结果保存。

- 增加 `scope: "workload"` 与必要的 ID/type；host 不带身份，container/workload 身份互斥。
- 在 API 边界解析 Pod，验证存在、类型和本次操作所需能力；保持原 host/container 请求。
- 对支持的 cgroup 目标枚举进程并执行已有 PID 身份校验，不要求先找到普通容器；不把 shim 自动当作采集进程。
- 将目标身份贯穿转发、执行及文档保存。能力不足明确报错，不回退 Host，不向 Guest 下发任务。

**验证**：扩展 scope、profiling validation/executor、API 用例。用目标 cgroup 内外各一个测试进程验证采样范围，检查最终文档身份；覆盖字段混用、缺字段、类型不符、目标不存在和执行前退出。旧 host/container profiling 回归通过。

### 15：tracing 支持 workload 目标

**本次交付**：适用的现有 tracing 操作接受 workload 目标并输出正确身份。

**主要位置**：tracing API、nodeagent tracing、具体执行入口和文档构造。

复用步骤 14 的目标规则，将 tracing 的目标传递、能力检查、执行和保存连通；各工具只声明实际具备的资源能力，不因接受了 workload 字段就宣称支持所有沙箱操作。

**验证**：沿真实请求路径检查 node/apiserver 参数转发、实际采集目标和落库文档；覆盖不支持的资源组合、旧 host/container 请求及目标退出。涉及多个独立执行器时逐个提交，未迁移的执行器明确返回不支持。

### 16：autotracing 逐项迁移

**本次交付**：每次迁移一个自动采集器的目标选择、实例状态和结果归属。

**主要位置**：`core/autotracing` 及关联的订阅、目标查询。

使用公共资源与实例状态；保持元数据刷新不重置冷却窗口，停止或删除后释放任务。保留 Kata 共享 VM 的现有选择及去重行为，不增加跨来源去重框架。

**验证**：分别验证触发条件、实际执行次数、采集目标和保存身份；不能只凭文档条数判断重复执行。覆盖刷新、订阅溢出、资源首次就绪、退出和发现恢复，提供涉及热路径的 benchmark。

## 第四阶段：CubeSandbox Guest 接入

### 17：GuestBridge 监听和连接核验

**本次交付**：GuestBridge 能识别来自具体 CubeSandbox 的合法连接。

**主要位置**：`internal/guestbridge` 的监听、peer identity、连接生命周期，Manager 中的连接核验信息查询。

- 增加 CubeSandbox 运行目录配置，按已发现对象监听 `/run/vc/vm/<sandbox_id>/cube.sock_<port>`。
- 按路径选择候选沙箱，以 `SO_PEERCRED` 对端 PID 与其 shim PID 一致作为接收前提；不信任固定 CID 或 Guest 自报身份。
- 仅使用 Manager 已有发现信息，不重复查询 cubelet，不将连接核验字段赋给 `Pod.Process`。
- 生命周期沿用现有监听/连接管理和限制；创建、退出、关闭时清理自身 socket，不误删其他进程的文件。
- 原 Kata 监听与认证保持兼容。本步通过内部集成入口验证；在步骤 18 完成文档接收前，不开放 CubeSandbox GuestBridge 生产配置。

**验证**：扩展现有 peer、Unix credentials、lifecycle 测试及 Unix socket 集成测试；覆盖未知沙箱、缺失凭据、无效/错误 PID、多个沙箱相同 CID、连接关闭与 socket 清理。真实 CubeSandbox 分别验证宿主机进程和 hostPID 容器部署下的对端 PID、运行目录挂载和连通性。

### 18：Guest 文档归属与 Host 保存

**本次交付**：通过已认证连接上报的 Guest 事件、tracing 和 profiling 文档可正确保存；开放 CubeSandbox Host 的 GuestBridge 配置。

**主要位置**：GuestBridge document receiver、tracing/profiling store 接入。

- 从认证连接和 Manager 补齐四个 workload 字段、Host 信息及 `source_type: "guest"`，不采纳 Guest 自报的 workload 或 container 归属。
- 保留 Guest 进程信息，不能拿 Guest PID/cgroup 查询 Host 资源；延续现有跨沙箱文档标识隔离，避免相同 Guest 本地任务 ID 冲突。
- 未知或未认证连接不保存数据，不能降级为无归属 Host 文档。
- 继续只接收已有文档类型，不增加指标协议或逐条保存确认。

**验证**：扩展现有 document receiver 与 profile ingest 集成测试。两个沙箱上报相同本地任务 ID，结果仍各归其主；伪造身份不能改变归属；Host 与 Guest 文档来源区分明确；原 Kata 文档保持 `kata_guest` 和容器字段。

### 19：CubeSandbox Guest 启动及整机采集接入

**本次交付**：Guest 可以使用通用镜像启动已有整机采集，并将数据通过 VSOCK 发给 Host。

**主要位置**：CLI 身份初始化、启动配置、`pod.go`、storage 和 document builder、采集器启停。

- 开放 `Mode="sandbox"`、`Pod.Source="cubesandbox"`、`GuestTransport.Enabled=true` 组合。
- 不要求 Kubernetes 身份环境变量，不查询 cubelet、不启动 Guest 内部应用扫描，也不为整机伪造一个容器。
- 正常存储必须使用 GuestTransport；保留 `--disable-storage` 调试例外，Kata Guest 的身份和存储要求不变。
- 复用现有整机事件、tracing、profiling 能力，Guest 发出本地文档，最终身份由 Host 补齐。接通启动链路不代表所有具体采集器已验收，剩余按步骤 20 逐项处理。
- 不启用 CubeSandbox Guest 业务指标输出或传输；保留事件和自动触发所需的内部资源读取。通用禁用发现开关不应把整机采集误当作内部应用发现而关闭。

**验证**：现有 sandbox、config、storage 测试覆盖合法组合、缺 GuestTransport、调试例外及 Kata 默认配置；在无 `BAMAI_*` 身份变量的 Guest 内启动，确认没有 cubelet 请求或应用扫描。至少触发一条真实整机事件，验证从 Guest 到 Host 保存的完整路径和进程信息，确认没有新增 Guest 指标链路。

### 20：Guest 采集能力逐项验证和补齐

**本次交付**：每次只补齐一个具体事件、tracing、profiling 或自动采集器在整机 Guest 模式下的问题。已经可用的能力只补验证，不为拆步骤而重复改代码。

按步骤 00 的清单分别验证：

- Guest 本地 tracing 请求采集的是 Guest 资源，结果经 Host 归属沙箱。
- Guest 本地 profiling 请求保留真实 Guest 进程和调用栈，不能采到物理宿主机进程。
- 已有适用的自动触发机制不再依赖发现出符合容器 ID 正则的应用；保留触发需要的本地数值读取，不增加对外指标。
- 与具体内核或资源能力不兼容的工具明确报告不支持，不能伪造成功。
- 断连、队列满和 Host 重启按既有语义丢弃未完成上报的数据并记录，自动重连后可发送新数据；不补传，也不将入队成功当作 Host 已保存。

**验证**：使用真实 Guest 的本地请求和负载，核对执行位置、执行次数、原始结果和 Host 存储；同时覆盖两个沙箱归属隔离及 Kata Guest 回归。传输故障复用现有 VSOCK 集成用例，检查重连、丢弃记录和后台任务清理。

## 第五阶段：收尾与版本验收

### 21：删除已无调用方的内部兼容入口

**本次交付**：结束内部双名称迁移状态，保留必要的对外兼容边界。

- 搜索全部旧 `Container` 管理、资源查询和生命周期接口的调用方，确认已迁移后删除适配入口；旧外部容器 API、指标和存储契约继续存在。
- 按设计目录整理剩余实现及其现有测试，公共层不再依赖 kubelet、Docker/containerd 或来源子包；不创建 OpenSandbox 空目录和空实现。
- 确认仅有一份 Store、一条同步循环，没有遗留后台任务或只有测试使用的生产占位框架。

**验证**：全仓库构建、`make unit`、`make check`，适用的 race 和性能回归。检查 Container、Kata 和 CubeSandbox 均经过同一公共管理实现。

### 22：部署说明和完整场景验收

**本次交付**：可按文档部署，并有覆盖本版承诺的验证记录。

**文档与部署改动**：补齐 Container 默认配置、CubeSandbox Host/Guest 配置、启动期字段、禁用发现别名、必要的 hostPID 与真实运行目录挂载，以及 API/指标/存储变化。检查现有 systemd、容器和部署模板是否满足要求，只修改实际需要的入口。

| 场景 | 必须证明的结果 |
| --- | --- |
| 普通 Container、Kata Host/Guest | 原有发现粒度、代表选择、指标标签、API、数据身份和采集结果保持兼容 |
| CubeSandbox Host，cgroup v1/v2 | 正确绑定各控制器；仅 cgroup 可采集；缺少进程、CSS、网络绑定时不误采其他资源 |
| 生命周期与故障 | 创建、暂停、恢复、退出、详情损坏、完整空集、5000 条截断及 cubelet 中断均遵循已确认规则 |
| 元数据与操作 | 元数据查询不触发外部 RPC；workload 目标身份和能力校验正确；Host 操作不进入 Guest |
| CubeSandbox Guest | 事件、tracing、profiling 以整个沙箱归属并保留进程信息，不依赖应用容器发现，不新增 Guest 指标 |
| 连接与部署 | 宿主机进程和 hostPID 容器均可连通；错误 PID、未知沙箱与伪造身份不能造成错误归属 |
| 上报异常 | 断连和满队列的丢弃可观察，重连继续发送新数据，不补传，不承诺 Host 保存确认 |
| 资源清理 | agent 停止、对象退出和连接关闭后无残留采集任务、文件描述符及自身监听 socket |

在具备真实依赖的 Linux 环境执行 `make build`、`make unit`、`make integration`、`make e2e` 和提交检查。逐项记录 PASS、FAIL、SKIP 及原因；本版要求的 CubeSandbox 和 Kata 核心场景必须有真实通过记录，不能仅凭模拟服务或 SKIP 宣布完成。

完成记录应包含：代码版本、Go 与内核版本、cgroup 模式、CubeSandbox 版本、部署方式、执行命令、结果位置和已知限制。方案已接受的周期发现时延、短生命周期漏发现及传输丢失边界仍然有效，不在验收时扩展为额外功能。

## 开发记录

### 步骤 00：接口与迁移清单

2026-11-05 开始实施，基线为 `a15e6a1b`。公共资源字段、首次就绪通知、Guest peer 信息传递和配置字段已补入方案文档的“实施接口细化”。当前工作区已有的 CSS 修复及其集成测试不属于本次重构，保持原状，不混入本次提交；基线测试使用该提交的独立副本。

以下清单来自当前代码引用。Host 表示 CubeSandbox 在宿主机的资源，Guest 表示 Guest 整机本地采集；“按能力”表示必须通过实际内核/工具支持检查，不能仅因完成接口迁移而宣称可用。“集成”表示该项没有对应独立单元测试文件时，应扩展已有相关用例或增加集成用例。

| 单独迁移对象（`core/` 下） | 所需资源 | CubeSandbox 范围 | 现有验证入口或新增验证方式 |
| --- | --- | --- | --- |
| `metrics/cpu_util.go` | cpu/cpuacct 或 unified，差值状态 | Host；Guest 无指标 | `cpu_util_test.go`、cgroup 集成 |
| `metrics/cpu_stat.go` | cpu/cpuacct 或 unified，差值状态 | Host；Guest 无指标 | `cpu_stat_test.go`、cgroup 集成 |
| `metrics/cpu_runqlat_tracing.go` | CPU CSS、实例状态 | Host 按能力；Guest 无指标 | cgroup/BPF 指标集成 |
| `metrics/system_loadavg.go` | CPU 路径及已有扩展能力 | Host 按能力；Guest 无指标 | 指标集成，缺扩展跳过 |
| `metrics/memory_vmstat.go` | memory/unified 文件 | Host；Guest 无指标 | 指标集成 |
| `metrics/memory_events.go` | memory/unified 文件 | Host；Guest 无指标 | 指标集成 |
| `metrics/memory_others.go` | memory/unified 及可选扩展文件 | Host 按能力；Guest 无指标 | `memory_others_test.go` |
| `metrics/memory_reclaim.go` | memory CSS | Host 按能力；Guest 无指标 | BPF 指标集成 |
| `metrics/iocost_collector.go`、`iocost_tracing.go` | blkio CSS 与 IO 数据 | Host 按能力；Guest 无指标 | `iocost_test.go`、`iocost_kernel_test.go` |
| `metrics/blk_throtl_collector.go`、`blk_throtl_tracing.go` | blkio CSS 与 IO 数据 | Host 按能力；Guest 无指标 | `blk_throtl_test.go`、`blk_throtl_kernel_test.go` |
| `metrics/iolatency_collector.go` | blkio CSS | Host 按能力；Guest 无指标 | `iolatency_collector_test.go`、BPF/kernel 用例 |
| `metrics/iocontrol.go` | IO 公共对象选择与标签 | 随上述单个 IO 采集器迁移必要接口 | `iocontrol_test.go` |
| `metrics/net_arp.go` | 可靠的 netns 访问进程 | Host 当前不具备；Guest 无指标 | 缺网络绑定集成 |
| `metrics/net_sockstat.go` | 同上 | 同上 | net 指标集成 |
| `metrics/net_netstat.go` | 同上 | 同上 | net 指标集成 |
| `metrics/netdev_stats.go` | netns、netlink 或 procfs | 同上 | netdev 指标集成 |
| `events/memory_oom_kill.go`、`memory_oom_kill_snapshot.go` | memory CSS 及文件快照 | Host/Guest 按能力 | `memory_oom_kill_test.go`、OOM 集成 |
| `events/memory_reclaim_events.go` | 当前事件使用的 CPU CSS | Host/Guest 按能力；不顺带改事件协议 | BPF 事件集成 |
| `events/net_dropwatch.go` | 事件携带的 cgroup/netns 关联 | Host 仅使用可信关联，Guest 本地整机 | `net_dropwatch_test.go`、dropwatch 集成 |
| `events/tcp_retransmit.go` | 同上 | 同上 | `tcp_retransmit_test.go`、网络事件集成 |
| `events/net_rx_latency.go` | netns cookie/inum 与原容器 QoS 筛选 | Host 无可靠 netns 时不归属，Guest 本地整机 | `test_events_net_rx_latency.sh` |
| `autotracing/cpuidle.go` | CPU CSS、目标进程及冷却状态 | Host/Guest 按能力 | `cpuidle_test.go`、`cpuidle_vm_test.go`、benchmark |
| `autotracing/dload.go` | 资源归属、目标进程及冷却状态 | Host/Guest 按能力 | `dload_test.go`、集成 |
| `autotracing/sched_blame*.go` | CPU CSS、订阅、进程目标 | Host/Guest 按能力 | `sched_blame_test.go`、supermonitor 用例、benchmark |
| `autotracing/memory_threshold_snapshot*.go` | memory 绑定、目录身份、订阅、进程枚举 | Host/Guest 按能力，Guest 无应用发现前提 | 现有 tracker/watch/target/action 单测与集成 |

未直接引用 `pod` 的采集器不能因此漏验：Guest 启动时还须逐项核对 `events/config.go`、`autotracing/config.go` 注册的整机工具，尤其 `cpusys`、`irqtracing`、`iotracing` 和 `memburst`。这些工具优先复用现有整机执行路径，仅在具体用例证明需要时修改；使用各自已有测试、CPU profiler 集成及真实 Guest 请求验证。

公共调用方分别跟随所属功能提交：

| 调用方 | 所属步骤 | 核对内容 |
| --- | --- | --- |
| `pkg/metric/data.go` | 06 | 容器标签兼容及 workload 固定标签 |
| `internal/matcher/container_matcher.go` | 对应采集器步骤 | 原容器筛选不变，不把容器专有字段当沙箱必填项 |
| `internal/document/builder.go`、`pkg/types/document.go`、存储映射 | 12 | 目标解析、顶层身份、历史记录兼容 |
| node/apiserver OpenAPI、handler、client | 11、14、15 | 元数据查询及操作目标全链路传递，生成代码同步 |
| `internal/nodeagent/profiling`、`pkg/profiling`、profiler 执行入口 | 14 | 校验、cgroup/PID 目标、结果保存 |
| `internal/nodeagent/tracing`、`pkg/tracing` 及具体工具入口 | 15 | 目标选择、能力声明、执行位置 |
| `internal/guestbridge` | 17、18 | 监听、peer 认证、原 Kata 兼容、Guest 文档归属 |
| CLI、config、Guest scanner、storage 初始化 | 05、19 | Mode/Source 组合、禁用发现、身份与存储前置条件 |

兼容基线以当前测试及源码为准，运行结果另记，不把静态核对称为实际采集通过：

- 指标公共标签沿用 `region`、`container_host`、`container_name`、`container_type`、`container_level`、`container_hostnamespace`、`host` 的既有常量与取值，定义位于 `pkg/metric/data.go`，迁移时逐项比较。
- 元数据接口沿用 `data.id` 和 `data.cgroup_css`，CSS 通过 `kernaddr.Format` 输出有效地址，未知容器保留现有错误。
- 容器文档仍由原 builder 填写 `container_id`、hostname、host namespace、type、QoS，Host 为 `host`，Kata Guest 为 `kata_guest`。
- 生命周期性能记录使用 `BenchmarkContainerEvents`、`BenchmarkContainerFullUpdate`、`BenchmarkContainerControllerRefresh` 和 `BenchmarkBuildCPURepresentatives`，保存 `ns/op`、`B/op`、`allocs/op`；未跑出结果前不填写估计值。

### 验证环境准备

本地为 macOS arm64、Go 1.24.3，`make build` 因缺少 Linux 工具 `nproc` 而失败，不作为 Linux 编译结果。按用户提供的测试机，使用 `/data1/huatuo-workload-20261105` 独立目录；不使用根分区保存构建缓存，不修改已有服务目录。

测试机为 Linux x86_64，内核 `4.18.0-193.6.3.el8_2.v1.4.x86_64`。已有系统 Go 1.23.8 和 Clang 12/libbpf 头文件不能直接完成当前基线构建。使用已有 Go 1.24.11 工具链，并在独立 Clang 18 容器准备现代 libbpf 头文件；生成工具及缓存放在任务目录。Unix socket 测试最初因临时目录过长失败，将测试临时目录缩短为 `/data1/hwtmp-20261105` 后通过；未修改业务代码绕过该限制。

基线验证结果（步骤 00）：

| 命令 | 结果 |
| --- | --- |
| `make build` | PASS，全部命令程序编译完成；静态链接的既有 dlopen 警告保留 |
| `go test -count=1 ./internal/pod/... ./pkg/metric/... ./internal/document/... ./internal/guestbridge/...` | PASS |
| `go test -race -count=1 ./internal/pod/...` | PASS |
| 生命周期 benchmark，`-benchmem -count=3` | PASS，原始结果保存在测试目录 `logs/baseline-bench.log` |
| `make check` | PASS，生成、依赖、格式化、lint 及差异检查通过 |

代表性 benchmark 基线（三次结果取中位数，`GOMAXPROCS=4`）：

| 用例 | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| ControllerRefresh / 1 个容器 | 2732 | 288 | 3 |
| ControllerRefresh / 1024 个容器 | 8026032 | 141799 | 1046 |
| BuildCPURepresentatives / runc | 128192 | 36992 | 6 |
| BuildCPURepresentatives / kata | 230845 | 36992 | 6 |
| ContainerFullUpdate / 4096 个容器 | 152801 | 0 | 0 |

步骤 00 的接口、清单和基线已完成；未进行真实 CubeSandbox/Guest 功能验收。下一步实施 Store 的 Pod 迁移，并与本基线比较。

### 步骤 01：Store 使用公共 Pod

代码增加 `workload.go`，将缓存实现拆入 `store.go`、实例采集状态移入 `resources.go`。同一条缓存记录发布 Pod 和只读 Container 兼容视图；保留容器 JSON、Kata 稳定选择时间和旧订阅行为。本步尚未替换来源调度、公共绑定校验或生命周期通知，不开放 CubeSandbox 来源。

采集状态由 Store 在实例创建时初始化，元数据更新保留状态，已识别的实例替换创建新状态。公共 Pod 成功同步时间独立更新，来源失败和 nil 成员不刷新；已经发布的视图不原地修改。`PodByID`、`Pods` 可读取公共对象，旧容器查询不返回沙箱。

验证记录位于测试目录 `logs/step01-*`：

- 全部程序 `make build`、pod/metric/document/guestbridge 单测、pod race 检查通过。
- 内核/运行时引用校验、Kata Host 不发布应用内存目标、HTTP 发现到内核绑定的三组集成测试通过，启用 race 检查，无 SKIP。
- `make check` 通过，新增测试覆盖兼容 JSON、元数据隔离、共享采集状态、实例替换、失败保留、完整空集和沙箱查询隔离。

补充验证发现基线的 `TestContainerControllerHTTPAndKernelBinding` 缺少 CRI 分类响应；原版本可复现 `containerd client not initialized`，补齐已有 fake runtime 的 running runc 响应后通过。此测试准备修复单独提交，不改变生产逻辑，也不使用真实节点容器作为测试对象。

同机 benchmark 对比（三次结果取中位数）：

| 用例 | 原 ns/op → 新 ns/op | 原 B/op → 新 B/op | 原 allocs/op → 新 allocs/op |
| --- | --- | --- | --- |
| ControllerRefresh / 1024 | 8026032 → 8455365 | 141799 → 420348 | 1046 → 3094 |
| BuildCPURepresentatives / runc | 128192 → 121364 | 36992 → 36992 | 6 → 6 |
| BuildCPURepresentatives / kata | 230845 → 212056 | 36992 → 36992 | 6 → 6 |
| ContainerEvents / 4096 / 8 订阅者 | 1712 → 1589 | 0 → 0 | 0 → 0 |
| ContainerFullUpdate / 4096 | 152801 → 143633 | 0 → 0 | 0 → 0 |

同步循环每次成功刷新为每个对象增加 Pod/记录快照，共约 272 B、2 次分配，以同时保留只读视图和最近同步时间；1024 个对象的全量同步约增加 0.43 ms。该成本发生在发现同步，不在 BPF 事件或指标循环中；事件发布和全量通知仍为零分配。其余耗时差异视为环境波动，不据此宣称性能提升。

### 步骤 02a：公共 cgroup 绑定

步骤 02 按资源拆分提交。本次增加 `cgroup.go`：从 mountinfo 解析控制器实际挂载位置，支持 v1 分挂载、合并挂载、v2、混合模式及绑定子树。`CgroupBinding.Directory` 返回可读取的目录，`Path` 返回对应成员路径和可用性，`ProcessDirectory` 按 unified/cpu/cpuacct/pids 顺序选择进程枚举目录。空路径、宿主机根路径、越界路径和非目录不能作为 workload 目标。

公共绑定按目录 inode 和控制器关联 CSS，不依赖容器 ID 或数字目录名称；来源自带 CSS 不作为绑定依据。CSS 缺失或冲突不影响文件读取。目录消失时查询立即失败；来源仍保留原定位信息，重新绑定可恢复读取。没有增加同路径重建识别机制。

Container Host 发现使用各控制器真实成员路径形成公共视图，旧 Container 字段及 Kata 应用内存目标排除规则保持兼容；Store 不再用旧共享路径覆盖已绑定的公共视图。待绑定位置保存在内部记录。进程/网络的公共校验、Guest 适配以及资源就绪通知仍由步骤 02 后续提交和步骤 03 完成，本次不代表步骤 02 全部完成。

验证命令和结果保存在测试机 `logs/step02a-*`：

- pod 单测及 race 测试通过，包括 CSS 冲突隔离和已发布绑定不可变性。
- 集成测试通过：v1/v2 挂载解析、控制器不同路径、目录延迟出现/删除、非目录与符号链接拒绝，以及既有 Container/Kata 引用校验。
- 在真实 v1 CPU/memory 挂载创建空测试组，并在独立挂载命名空间创建真正的 v2 挂载，两组测试均通过，无 SKIP；不迁移进程、不修改资源限制，测试组已清理。
- `make build` 通过；提交检查使用 `make check`，原始日志为 `step02a-check.log`。
- 本次未修改指标或 BPF 事件处理循环。ControllerRefresh/1024 的三次中位数为 8358717 ns/op、469507 B/op、3094 allocs/op；相比步骤 01，记录和公共绑定字段使每个对象刷新增加约 48 B，分配次数不变，耗时差异视为环境波动。

### 步骤 02b：公共进程与网络绑定

Container Host 的公共资源校验增加进程和网络部分，旧 Container 对外字段保持原样。`Pod.ProcessPID` 只返回已绑定且启动时间仍一致的代表进程；`Pod.NetworkPID` 另行验证网络目标进程及 netns inode，不从 IP 或代表进程推导网络目标。来源提供的 cookie 重新在本机核验；旧内核不支持 cookie 时仍可使用已验证的 inode。

绑定缺失时查询返回 `0` 和错误，不回退到 PID 1 或默认网络命名空间。只有 IP 的对象保留地址元数据，不发布网络采集能力。进程和网络的任一失败不清除已有 cgroup。进程启动时间、proc 根目录只作为内部校验信息，不进入 JSON。Kata Guest 扫描的公共资源适配仍在步骤 02 后续提交中完成。

测试机 `logs/step02b-*` 记录本次验证：pod race 测试、真实子进程/netns 集成测试、既有 Container/Kata 回归、v1/v2 挂载回归和 `make build` 均通过，无 SKIP。新增用例覆盖 PID 不存在、启动时间变化、进程退出、netns 变化、IP-only、网络 PID 与代表进程不同，以及 cgroup-only 对象不会获得宿主机进程目标。提交检查使用 `make check`，日志为 `step02b-check.log`；没有修改当前指标或 BPF 事件循环。

### 步骤 02c：Kata Guest 公共资源适配

现有 Guest 扫描保留原来的代表进程选择、容器 ID 识别和私有 cgroup 命名空间解析。扫描结果同时保留各控制器成员路径，依据 Guest 的 mountinfo 经过公共绑定后发布 Pod；不再由宿主机运行测试时的 cgroup 模式决定 Guest 公共视图。CPU、cpuacct、memory、IO 可以各用自己的路径，私有命名空间中的 IO 路径按对应挂载解析。进程通过 Guest 的 proc 树验证，没有 netns 定位时不发布网络目标。

原 Container 和内存引用字段保持兼容。绑定完成后再次核对进程启动时间及全部控制器成员路径，避免把扫描期间已变化的位置发布出去。公共挂载信息暂不可读时不推测目录，既有容器兼容视图仍保留。

本次验证记录在 `logs/step02c-*`：pod race 测试、Container/Kata 绑定回归、真实 v1/v2 挂载和进程/netns 集成测试、`make build` 通过；提交检查使用 `make check`，日志为 `step02c-check.log`。Guest 扫描使用已有 proc/cgroup fixture 补测分控制器及 v1/v2 私有命名空间，本次没有宣称真实 Kata VM 端到端验收完成。

步骤 02 的公共绑定与已有发现适配已完成。下一步处理公共生命周期订阅和资源首次就绪通知；CubeSandbox 来源及其采集链路尚未开放。

### 步骤 03a：资源首次就绪保持实例状态

Store 不再将原来未知的 PID、进程启动时间、内存路径或目录首次补齐视为重启。公共 cgroup、CSS、进程和网络能力变化复用全量通知，保持原实例代次与采集基线；重复同步和仅名称/IP 更新不产生额外通知。

内存监视器同时适配同代次的完整引用变化，以及目录尚未就绪的记录。已注册的监视和待处理压力事件在重复通知时保留；既有注册失败和容量限制策略不变。本次尚未更换公共订阅入口或 cgroup 事件归属索引。

测试机 `172.16.135.140` 的 `logs/step03a-*` 保存验证：pod/autotracing race 测试及完整构建通过，原生内存阈值内核通知集成测试通过。该集成测试加 `-race` 时，其子进程触发测试自身的 128 MiB cgroup 限制而被 OOM 杀死，内核日志已确认；按原生方式运行通过，单测继续使用 race。提交检查使用 `make check`。

同机三次 benchmark 中位数对比：

| 用例 | 原 ns/op → 新 ns/op | 原/新 allocs/op |
| --- | --- | --- |
| ControllerRefresh / 1024 | 8414988 → 8420203 | 3094 / 3094 |
| ContainerEvents / 4096 / 8 订阅者 | 1622 → 1614 | 0 / 0 |
| ContainerFullUpdate / 4096 | 152943 → 151815 | 0 / 0 |
| MemorySnapshotContainerFullUpdate / 4096 | 181844 → 219396 | 0 / 0 |

内存监视器全量核对增加 PID、路径和待绑定状态比较，4096 个对象约增加 38 μs，仍为零分配；不改变内存压力事件处理循环。其余耗时差异视为环境波动。

### CubeSandbox 实机环境补充

已直接免密连接 `172.16.134.96`，内核为 Linux 5.10、使用 cgroup v2。cubelet socket 为 `/data/cubelet/cubelet.sock`，现有 `cubecli inspect` 确认详情包含 `CGroupPath=/cube_sandbox/sandbox/8`、shim PID 和纳秒时间戳；对应目录包含 CPU、memory 和进程文件。代码参考 `/Users/didi/code/CubeSandbox`，未修改该仓库。

检查时现有沙箱为 Paused，运行中列表为空，因此尚未完成运行中沙箱的采集验证；后续接入仍严格按运行状态筛选。本次仅查询现有环境，没有启动、恢复或删除现有沙箱。

### 步骤 03b：公共 Pod 订阅

新增 `SubscribePods`、`PodSubscription`、`PodKey`、`PodEvent` 和 `PodUpdate`。公共订阅与旧 `SubscribeContainers` 复用 `subscription.go` 中同一有界队列实现；旧入口只投影 Container 引用，不向未迁移调用方泄漏沙箱。身份和事件种类语义相同的旧类型使用兼容别名，旧返回结构保持原样。

首次发现尚未建立缓存时仍返回不可用错误；缓存建立后，公共订阅通过 `PodUpdate.DiscoveryError` 报告发现异常，同时交付保留的对象。旧容器入口保留原错误契约。队列保存发布时的引用值和不可变 Pod 视图，完整快照清除已覆盖的增量，慢订阅者溢出后仍使用全量恢复，关闭不改写借出结果。

`logs/step03b-*` 记录 pod/autotracing race 测试、完整构建和内存阈值内核通知集成测试通过。新增测试覆盖公共/容器入口隔离、首次发现失败、非空及空缓存的错误恢复、cgroup-only 资源就绪、队列身份稳定和关闭。提交检查使用 `make check`。

同机三次 benchmark 中位数：旧接口 8 个订阅者的单次事件投递由 1614 ns 增至 1909 ns；4096 个对象的全量通知由 151815 ns 增至 195856 ns。新增公共接口的 8 订阅者投递为 1989 ns。三者均为零分配；额外耗时来自公共队列的类型过滤与兼容结果转换，没有新增后台同步循环。

### 步骤 03c：按公共资源归属 cgroup 事件

Store 为已验证的 CSS 和内核 cgroup 地址维护反向索引，提交、资源变化、成员删除及停止时同步更新。内核地址仅保存在内部，不进入 Pod JSON。数字目录名不再影响事件归属；未知事件和共享资源只请求合并的全量刷新，不依据目录名称创建或删除对象。

删除事件按实际目录使对应绑定失效：v1 保留其他挂载上的控制器，v2 清除同一目录上的能力。失效以新视图通知订阅者，保留原实例代次、采集状态和最近成功同步时间；来源查询失败时仍可通知。Kata 多个容器共享资源时一起失效，旧 Container CSS 视图同步更新。没有增加同路径重建或资源替换机制。

`172.16.135.140` 的 `logs/step03c-*` 记录 pod/autotracing race 测试、完整构建和真实 v1 内核集成测试通过。集成测试创建独立数字名称的空 cgroup，验证 CSS 探针、公共绑定、BPF 删除事件到全量通知的完整链路，随后清理；不迁移进程、不修改已有资源限制。同一测试二进制在 `172.16.134.96` 的真实 v2 环境运行通过，无 SKIP，日志为该机任务目录下的 `logs/step03c-kernel.log`；现有 CubeSandbox 未被启动、恢复或修改。提交前运行 `make check`。

4096 个对象下三次 benchmark 中位数：cgroup 地址归属查询 156.8 ns/op，CSS 归属查询 187.6 ns/op，未知事件 75.33 ns/op，均为零分配；删除事件需要复制被影响对象的只读元数据。公共事件投递（8 订阅者）为 2016 ns/op、零分配。ControllerRefresh/1024 为 8421295 ns/op、485891 B/op、3094 allocs/op；相比此前约增加 16 KiB 的快照空间，分配次数未增加，耗时差异视为环境波动。

步骤 03 已完成。下一步将 Container 来源接入统一 Manager，继续保持现有容器对外契约；当前尚未开放 CubeSandbox 来源或宣称完成沙箱采集验收。


### 步骤 04a：抽出公共发现调度

将唯一的同步循环、周期与退避、刷新提示合并、取消和停止清理移入 `manager.go`。Host Container controller 保留 kubelet 列表解析和现有逐对象就绪重试；Kata Guest 直接使用公共调度，不再创建 Container controller。没有增加第二条同步循环，现有对外入口、周期和重试次数保持不变。

本次先拆开调度与来源实现，暂以内部同步回调衔接；`Source.Discover` 的公开边界、发现结果验证和 Container 子包迁移仍属于步骤 04 后续提交，不代表步骤 04 全部完成。

验证记录位于 `172.16.135.140:/data1/huatuo-workload-20261105/logs/step04a-*`：pod/autotracing race 测试和完整构建通过。新增用例覆盖慢查询期间提示合并到下一轮、取消后不提交返回结果及停止清理；保留现有有界就绪重试、删除确认、Guest 扫描和订阅关闭回归。另运行 HTTP/CRI 到真实内核绑定的集成测试及提交检查 `make check`。本次只移动调度职责，不改变 BPF 事件处理和指标循环。


### 步骤 04b：修正发现成员与失败提交语义

Container 发现不再静默覆盖重复运行 ID。无论重复发生在同一 Kubernetes Pod 内还是不同 Pod 间，该 ID 本轮均保留为 nil，结果标记为不完整并报告错误；保留旧视图，不根据缺失项删除已有对象，其他有效记录仍可提交。

已有对象的详情解析失败或有限重试耗尽时，同样以 nil 成员保留上次成功视图，避免将旧记录当作本轮成功结果而刷新 `Pod.SyncedAt`。成员列表仍然完整时，其他已退出对象照常删除；Kata 持续重试和健康容器的缓存复用保持原行为。

验证记录在同一测试目录 `logs/step04b-*`：pod/autotracing race 测试、HTTP/CRI 与真实内核绑定集成测试、完整构建通过；提交检查使用 `make check`。新增用例覆盖重复成员、有效新成员继续提交、失败保留、重试耗尽、Kata 故障恢复与无关对象删除。ControllerRefresh/1024 三次中位数为 8596482 ns/op、485895 B/op、3094 allocs/op；分配次数与此前一致，新增判断发生在发现同步中。

步骤 04 尚未完成：后续仍需接入 `Source.Discover`、统一来源结果校验和超时控制，并迁移 Container 来源子包及启动调用方。CubeSandbox 来源尚未开放。


### 步骤 04c：发现截止时间与取消传递

公共 Manager 为初始化和每轮发现分别创建默认 10 秒的 context，将其传给 kubelet/configz、Docker 信息请求及 CRI 状态查询。现有单请求 5 秒限制继续生效，但不再用独立后台 context 隔断调用方取消。配置接口留在步骤 05 接入。

超过本轮截止时间后，即使来源返回完整空列表或 nil 错误，也丢弃该轮结果，保留缓存与上次成功同步时间并退避重试。重试恢复全量标记，避免已被来源消费的刷新提示随超时丢失。初始化超时不进入发现；取消 configz 时不再继续走本地配置兜底或触发配置缺失 panic。查询继续串行执行，不为超时调用再启动一条并发同步。

现有 `k8s.io/cri-client` 的首次客户端创建不接收 context，其连接和版本检查仍按原有 5 秒上限完成，不能保证父 context 取消后立即返回；后续 RPC 已继承取消。Manager 会等不可中断调用返回再丢弃过期结果，不宣称所有底层调用都可在截止时立即中断。未修改 vendored 库，也未改变长期客户端的单请求超时。

`logs/step04c-*` 保存 pod/autotracing race 测试、完整构建、现有 HTTP/CRI 到内核绑定集成测试，以及新增真实 HTTP 取消集成测试。用例覆盖迟到的完整空列表、初始化迟到成功、缓存保留、恢复全量刷新、CRI 取消和 configz 取消；提交检查使用 `make check`。

步骤 04 仍需完成 `Source.Discover` 结果边界、来源类型与 ID 校验，以及 Container 子包和启动调用方迁移。当前改动为这些迁移准备了公共调度、正确的失败语义和取消链路，尚未开放 CubeSandbox 来源。


### 步骤 04d：统一来源接口与 Manager 入口

新增 `Source.Type/Discover`、`Discovery`、`GuestPeer` 及 `StartManager(Source, ManagerOptions)`。新入口复用已有唯一调度循环、超时、退避与停止清理，不创建来源注册表或第二个 Store；重复启动明确报错。零周期选项使用既定默认值，负值拒绝，来源选择的配置接入仍在步骤 05 完成。

Manager 校验 map key、Pod ID 和来源类型。不匹配或空 ID 的结果不能授权删除，其他有效记录仍可提交；nil 成员、部分结果和来源错误按原约定处理。来源提供的 CSS、采集状态和同步时间不能直接进入缓存，公共绑定层重新验证各控制器、进程和网络，Store 生成实例状态与成功同步时间。

Guest peer 与有效 Pod 同次提交，新增 `GuestPeerByID` 只读查询，同次返回实例 Key 与 peer，供连接方核对订阅实例是否仍然有效。nil 成员及发现失败保留旧 peer，有效更新缺失或无效 peer 时清除，对象删除一起清理；孤立 peer 不创建身份，也不成为 Process 或 Network。peer 能力变化复用全量通知。非容器对象以已确认启动时间识别重启；首次补齐或暂缺启动时间保留既有实例与已知身份，不引入资源替换机制。

公共 Pod 到旧 Container 的投影保留原字段结构、Kata CPU CSS 范围和内存目标排除规则。旧视图需要可安全表示的 CPU/memory 位置及必需容器元数据，避免空路径读到宿主机根目录或缺失 namespace label 触发旧构造器 panic；公共 Pod 本身仍保留独立可用资源。独立网络 PID 不会被伪装成旧结构中代表进程的网络目标。

本次新增统一入口并验证接口边界，**旧 `InitManager` 和 `InitGuestManager` 尚未切换**。现有 Container/Guest 发现仍走已验证的旧适配器；后续提交迁移真实来源及启动调用方，不把当前合成来源测试视为 Container 子包或 CubeSandbox 接入完成。

`logs/step04d-*` 记录 pod/autotracing race 测试、完整构建、HTTP 取消及旧 Container 内核绑定回归通过。新增 Source 集成测试通过真实 v1 挂载验证发现、文件绑定、资源消失保留实例及完整空集删除，测试只创建独立空组并清理；同一测试在 `172.16.134.96` 的真实 v2 环境通过，无 SKIP，日志为该机任务目录的 `logs/step04d-cgroup-v2.log`。未操作现有沙箱。提交检查使用 `make check`。

三次 benchmark 中位数：旧 ControllerRefresh/1024 为 8422405 ns/op、502279 B/op、3094 allocs/op；保存 peer 的记录增加约 16 KiB 快照空间，分配次数不变。公共事件投递（8 订阅者）为 1981 ns/op、零分配。新增 SourceDiscovery/1024 为 1429635 ns/op、约 803980 B/op、6048 allocs/op，包含读取实际 mountinfo、公共绑定与提交，但使用无进程及无 cgroup 位置的元数据对象，不代表真实来源 RPC 或大量 cgroup 文件采集性能，也不与旧 Controller 的耗时直接比较。

### 步骤 04e：真实发现使用公共资源绑定结果

普通 Container runtime 解析和 Kata 定位不再查询 CSS 或网络命名空间，只提供已有身份和位置。Host 绑定完成后，旧 Container 视图的 CSS、网络 inode/cookie 从同次公共 Pod 绑定结果生成；Guest 扫描使用同一投影，移除按容器 ID 单独获取 CSS 的路径。保留旧 cgroup 字段与时间语义、空 CSS 的 JSON map 形态、Kata 仅 CPU CSS、Host VM 不成为 Guest 网络目标及应用内存目标等规则。

本次覆盖现有生产发现链路。`containerMemoryBinding` 在更新兼容视图前复制对象，避免 `ValidateContainerRef` 重校验修改正在被采集器读取的缓存。缺失 CSS、netns 不影响其他已绑定资源。Guest 的 CSS 由实际目录 inode 匹配，即使内核缓存没有容器 ID 也可完成归属。

验证记录位于测试机任务目录 `logs/step04e-*`：pod/autotracing race、HTTP 发现与内核绑定、进程/目录/代次变化校验、Kata Host 内存排除及公共 cgroup 绑定集成测试、完整构建。扩展已有用例覆盖 CSS/netns 缺失、已发布视图不变、Guest v1/v2 兼容视图共享同一 CSS。实际 Kata runtime 集成用例同步改为经过公共绑定后断言；本次未提供运行中的 Kata 实例，不将该用例记为实机通过。提交前运行 `make check`。

来源与启动入口的最终迁移仍未完成：本次先移除真实来源中的重复资源查询，不将旧调度回调转换成隐藏的 Source 适配层。

GuestScan（三次、32 进程合成 /proc 树）中位数为 1630145 ns/op、约 152647 B/op、1510 allocs/op；公共事件投递仍为零分配。这里只记录本次成本，不据此声称相对旧版本提升。

### 步骤 04f：CSS 初始化失败不阻断 Host 发现

旧 Host 启动入口在 kubelet 查询配置成功后，CSS 初始化失败只记录告警并继续发现。行为与 Guest 入口、公共 Source 入口一致；基础文件指标与元数据不依赖 CSS 探针能力，不额外增加初始化线程。kubelet 或 runtime 的错误处理保持原有重试语义。

新增生产启动路径集成用例，使用真实 HTTP 和本机 procfs、模拟 CSS 初始化失败，确认 Host 仍发布容器和内核绑定；同时回归 Guest 同场景。`logs/step04f-*` 记录上述集成测试、pod/autotracing race、完整构建及提交检查 `make check`。本次不启动或改动测试机上的现有沙箱。

### 步骤 04g：迁出 kubelet 协议查询

将 PodList/configz HTTP 查询、响应大小限制、错误正文截断及配置响应结构迁入 `internal/pod/container/kubelet.go`，不增加客户端接口或后台任务。原 HTTP 测试文件随实现搬迁为 `container/kubelet_test.go`；依赖旧初始化状态的取消用例留在公共包已有测试文件中。

本次是分阶段搬迁：旧初始化适配器暂时调用子包的协议函数，子包目前不依赖根包。后续增加返回公共 Pod 的 Container Source 前，必须将其余来源初始化与调用方一起移出根包，消除这条临时反向调用，不能形成根包与来源子包相互导入。

测试机 `logs/step04g-*` 记录根包、Container 子包和 autotracing race 测试通过；HTTP 发现、整轮取消、无 CSS 的生产启动路径集成测试以及完整构建通过。提交前运行 `make check`。步骤 04 的 Source 和启动入口最终切换仍待完成。

### 步骤 04h：kubelet 连接状态归来源实例所有

`container.Kubelet` 接管只读端口探测、证书加载、HTTPS 回退和选定的 HTTP 连接。旧 Host 入口创建实例并将 `Pods` 查询注入 controller，移除根包的 kubelet URL、HTTP client、启用状态三个全局变量及私有证书字段。实例不启动后台任务，初始化失败不发布连接状态，由现有 Manager 重试。保留单文件证书和逗号分隔证书/私钥的配置方式、只读端口优先及原 TLS 校验行为。

配置文件兜底和 runtime 配置暂仍由旧初始化适配器处理，后续随来源一并迁移。新增已有测试文件中的 HTTP/TLS 用例覆盖端口选择、客户端证书、configz、未初始化调用及取消后不触发证书回退；根包取消回归继续覆盖不误入本地配置兜底。`logs/step04h-*` 记录 root/container/autotracing race、生产启动及 HTTP 取消集成测试、完整构建和提交检查 `make check`。

### 步骤 04i：迁移 kubelet 配置兜底，保留退出行为

按用户确认，configz 查询失败且所有默认本地 kubelet 配置不可读或不可解析时，仍沿用原有 panic 退出行为，不改成 Manager 重试。`container.Kubelet.MustConfiguration` 接管远端优先、本地文件顺序兜底和该失败策略；取消或超时仍返回 context 错误，不误入本地配置兜底。根包只暂存旧 runtime 初始化所需的 driver/endpoint，尚未完成 Source 切换。

原 `kubelet_panic_test.go` 随实现迁入来源子包，替换此前未实际执行目标函数的占位测试，覆盖真正的 panic、取消不 panic、配置优先级、损坏/缺失文件跳过以及空配置保持默认字段。生产启动集成测试改用真实 HTTPS configz 和证书，不依赖根包配置路径全局变量。`logs/step04i-*` 记录 pod/container/autotracing race、HTTP/HTTPS 发现与取消、无 CSS 启动、完整构建及 `make check`。

### 步骤 04j：迁移 Docker/containerd 协议及 PID 解析

将 Docker Info、containerd CRI 初始化及状态解析、Docker 配置文件和 containerd PID 文件读取迁入 `container/runtime.go`。根包暂时保留现有 provider 状态和旧查询适配入口，调用同一份来源实现；保持短 ID 匹配、歧义拒绝、运行态核验、Kata Host PID 选择及原有超时语义。未新增 runtime 注册接口或另一套实例缓存。CRI 构造函数不能继承上层 context 的既有 5 秒边界仍保留，不修改 vendor。

原 PID 文件测试迁为 `container/runtime_test.go`，CRI 状态解析用例同步迁入，根包保留旧调用链路的 Kata 分类和实例校验回归。新增外部依赖集成测试使用原生 Docker HTTP 客户端和原生 CRI Unix socket gRPC 客户端，验证连接、状态目录、Kata 元数据、异常配置不发布结果及取消传递。测试服务是本次独立 fixture，不操作现有容器或沙箱；不能据此宣称真实 CubeSandbox 已接入。

`logs/step04j-*` 记录 pod/container/autotracing race、HTTP/CRI socket 集成、旧 Host 生产启动、内核实例校验、完整构建及 `make check`。真实 `Source.Discover` 和启动调用方切换仍未完成，根包到协议子包的迁移期依赖仍待在该切换时消除。

### 步骤 04k：Host 容器发现切换到 Source

现有 `InitManager` 入口现在构造真正的 Container `Source`，通过公共 Manager 的 `Discover → bindDiscovery → Store` 链路发布结果。删除旧 controller 的逐成员缓存、三次重试上限和基于提示跳过健康成员的分支；每轮重新读取完整成员及元数据，周期、退避、超时和停止仍由唯一 Manager 循环负责。CSS 提示只提前触发下一轮完整发现，不直接决定成员增删。

来源保留运行中普通容器和可重启 init sidecar 的筛选规则。重复 ID、身份解析失败和缺失 spec 将结果标为不完整；详情失败保留 nil 成员，仍可删除完整列表中已经消失的其他成员。查询失败和取消不能发布完整空集。Kata 运行时恢复不受重试次数限制，Store 继续保持同一实例的采集状态和代表对象优先级。

来源提供实际 `/proc/<pid>/cgroup` 控制器位置，由公共 Manager 验证目录、进程、网络及 CSS。`ContainerInfo.CgroupPath` 暂存旧视图使用的共享路径，不序列化到公共元数据；旧 Container API 和旧采集器继续使用原路径，公共 Pod 使用真实控制器绑定。两者不共用路径字段，避免这次切换静默改变旧接口。缺失资源不回退到宿主机根 cgroup。configz 与默认文件均不可用时的原有 panic 行为保持不变。

测试机 `172.16.135.140` 的 `logs/step04k-*` 记录 pod/container/autotracing race、真实 HTTP/HTTPS 生产入口、CRI socket、内核绑定、缺失 CSS、删除先于 kubelet 更新及整轮取消验证。基准使用本地内存中的 kubelet 列表和元数据解析 fixture，包含公共绑定与 Store 提交：1024 个容器完整刷新约 15.1 ms、3.70 MB、32711 次分配；不包含真实 kubelet/CRI 往返，不能作为真实节点容量结论。完整构建和提交前 `make check` 单独记录。

步骤 04 尚未全部完成：Container Source 暂放在根包以复用迁移中的 runtime 状态；仍需完成来源子包的依赖方向、启动调用方注入，以及现有 Kata Guest 本地扫描接入 Source。本次只完成 Host 生产发现链路切换，未开放 CubeSandbox 来源。

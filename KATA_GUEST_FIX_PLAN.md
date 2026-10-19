# Kata Guest 支持代码审查修复计划

- 分支：`feat/kata-guest-support`
- 审查范围：`a353eb20^..HEAD`（16 commits，HEAD `68dbb919`）
- 基线：16 commits / 137 files（+9475 / −211，不含 vendor 为 +8703 / −208）
- 结论：**0 P0 / 8 P1 / 若干 P2**（R1 复核后由 P1 下调为 P2，见下文）
- 说明：行号以 HEAD `68dbb919` 为准；当前 checkout 缺少生成物（`internal/bpf/abi`、`event.capnp.go`、mock），完整 `make check` 需先执行 `make gen-build`。

## 优先级定义

| 级别 | 含义 | 处理要求 |
|---|---|---|
| P0 | 数据损坏、安全漏洞、主流程不可用 | 本次未发现 |
| P1 | 数据丢失、可被触发的错误、CI 失败、明显性能回退 | 合并前必须修复 |
| P2 | 可维护性、一致性、测试/文档缺口 | 合并前尽量修复，可拆后续 PR |

---

## 总览

| ID | 优先级 | 问题 | 影响面 | 工作量 | 建议提交 |
|---|---|---|---|---|---|
| R1 | P2 | VSOCK `Close` 返回值不反映已丢弃队列 | 关闭语义/可观测性 | S | `fix(vsock): report dropped documents on close` |
| R2 | P1 | Unix socket 按路径清理，可能删除他人刚绑定的 socket | Host 监听可用性 | M | `fix(bridge): only unlink the socket this listener bound` |
| R3 | P1 | 未使用常量导致 lint/CI 失败 | CI | S | `fix(pod): remove unused guest test constant` |
| R4 | P1 | `ChunkFrameSize` 与 `SendChunk` 重复完整编码 | Guest CPU/内存 | M | `refactor(transport): encode each chunk once` |
| R5 | P1 | 单个坏 Guest 容器冻结整个视图的删除并置全局不可用 | Guest 全量指标/事件 | S | `fix(pod): keep guest membership when binding fails` |
| R6 | P1 | Kata CPU CSS 解析失败被当成容器发现失败 | Host 全量容器视图 | S | `fix(pod): do not fail Kata discovery without CPU CSS` |
| R7 | P1 | CSS 缓存覆盖所有 cgroup，解析为 O(containers × cgroups) | Host CPU/内存 | M | `perf(pod): index CSS cache by container and inode` |
| R8 | P1 | Kata 容器每次 refresh 都走一次 CRI RPC | Host 发现延迟/API 压力 | M | `perf(pod): reuse Kata runtime binding across refreshes` |
| R9 | P1 | Sandbox 模式无 provider，Guest 容器无法解析 PID/cgroup | Guest profiler | M | `fix(pod): resolve guest PID and cgroup from published records` |

P2 见文末批次表。

---

## P1 详细计划

> R1 复核后下调为 P2，见“P2 详细计划（复核项）”。

### R2. Socket 清理竞态

**现象**：`server.Close()` 会等待 handler 退出；这期间另一个 agent 进程可重新 bind 同一 `kata.hvsock_<port>`，随后被旧的清理逻辑按路径删除。

**根因**：
- `socketListener.Close` 直接 `os.Remove(l.path)`（`internal/guestbridge/connection_peer_linux.go:108-115`）。
- `bridge.removeListener` / `closeListeners` 在 `server.Close()` 之后又按路径删除一次（`internal/guestbridge/bridge.go:449-457`、`:500-513`）。
- `removeStaleSocket` 的 dial → `os.Remove` 存在 TOCTOU（`connection_peer_linux.go:79-89`）。
- 仓库已有正确实现：`internal/toolstream/transport/uds.go:72 removeSocketIfSame`（lstat + `os.SameFile`）。

**修复步骤**：
1. 将 `removeSocketIfSame` 提升为可复用工具（实际实现为 `internal/utils/fileutil.RemoveIfSame`），供 guestbridge 使用。
2. `listenFirecracker` bind 后立即 `os.Lstat(path)`（chmod 前），把 `os.FileInfo` 存入 `socketListener`。
3. `socketListener.Close` 改为 `fileutil.RemoveIfSame(l.path, l.fileInfo)`。
4. 删除 `bridge.removeListener`、`closeListeners`、`scanFirecracker` 错误路径中的重复 `os.Remove(entry.socketPath)`，由 listener 统一负责。
5. `removeStaleSocket` 记录 lstat 信息后使用 `fileutil.RemoveIfSame` 删除。

**测试**：
- 在 `TestLongFirecrackerSocketPath` / `TestFirecrackerListenerLifecycle` 中：绑定 socket A → 关闭 A 的 listener（模拟 handler 阻塞期间）→ 绑定 socket B 到同路径 → 完成 A 的 `server.Close()` → 断言 B 仍存在且可 Accept。
- `removeStaleSocket` 用例：探测后替换路径，断言不删除新文件。

**验收**：关闭旧 listener 永不删除已替换的路径。

**实施记录（2026-10-19）**：
- 新增 `fileutil.RemoveIfSame`（`internal/utils/fileutil/fileutil.go`），`transport.udsListener` 与 `guestbridge.socketListener` 统一使用；`removeStaleSocket` 改用 lstat 快照 + `SameFile` 删除。
- `socketListener` 保存 bind 后的 `FileInfo`；删除 `bridge.removeListener`/`closeListeners`/`scanFirecracker` 中重复的路径删除与 `listenerEntry.socketPath` 字段。
- 新增 `internal/guestbridge/connection_peer_linux_test.go`（正常清理 + 替换 socket 保留）与 `fileutil.RemoveIfSame` 表驱动测试。
- 已验证：`go test -race ./internal/utils/fileutil/...` 通过；`gofmt`/`gofumpt`/`git diff --check` 干净。`guestbridge`/`transport` 因本机缺少生成文件（`event.capnp.go`、`internal/bpf/abi`）无法编译，需在 CI 执行 `make gen-build && make check`。

---

### R3. 解除 lint/CI 失败

**现象**：`internal/pod/guest_scanner_test.go:32` 的 `guestPaymentID` 未使用；`.golangci.yaml` 启用了 `unused`(U1000)，预计 `make check` 失败。

**修复步骤**：
1. 删除 `guestPaymentID`（或补一个使用它的测试，但当前无场景，删除更干净）。
2. 在 Linux 环境执行 `make gen-build && make check`，记录基线；若出现其他 lint 报错，纳入本计划同一 PR 或单独 PR。
3. 在 CI 中确认 `make unit` 也通过（`unused` 仅在类型检查成功的包上报错）。

**验收**：`make check` 零报错。

**实施记录（2026-10-19）**：
- 删除 `guestPaymentID`（`internal/pod/guest_scanner_test.go`）。
- 对本次新增的第一方 Go 文件做了未使用标识符扫描（const/var/func/type 按包统计引用），未发现其他孤立声明。
- 本机无法执行 `make check`：缺少 `bpf/*.o`（`bpfabi-tool` 需要）与 capnp 生成物，`go test ./internal/pod` 在 typecheck 阶段因 `internal/bpf/abi` 缺失而失败。需在 CI 执行 `make gen-build && make check` 确认。

---

### R4. 单次编码 Chunk 帧

**现象**：每个文档 `ChunkFrameSize` 完整构建并 marshal 一次，`SendChunk` 再构建并 marshal 一次；同时 `Save` 会复制一次 `rec.Data`。16 MiB 文档峰值约 3 份数据。

**根因**：
- `internal/toolstream/transport/client.go:175-200` 为测量构造完整消息并 `Marshal`。
- `internal/toolstream/transport/client.go:96-108` 发送时重复构造。
- `internal/storage/vsock/vsock.go:118-141` 先测尺寸再入队，随后 `send` 再编码。

**修复步骤（推荐）**：
1. 新增 `transport.EncodeChunkFrame(data []byte) ([]byte, error)`：内部只构建/编码一次，返回完整帧。
2. VSOCK 队列从 `queuedDocument{data []byte, size int}` 改为 `queuedDocument{frame []byte}`；`Save` 调一次 `EncodeChunkFrame`，检查 `len(frame) <= MaxMessageSize` 与队列上限后入队。
3. `send` 在握手完成后直接 `conn.Write(frame)`（需保证写完整帧；保留写 deadline），不再调用 `SendChunk`。
4. 保留 `Client.SendChunk` 给其他调用方；`ChunkFrameSize` 可删除或标记 deprecated（若无其他调用点）。
5. 更新 `docs/development/toolstream_transport.md` 中的 `ChunkFrameSize` 描述。

**测试**：
- `TestEncodeChunkFrameMatchesWireFormat`：编码结果可被服务端 `messageDecoder` 解析，且 `len` 与服务端统计一致。
- `TestSaveEncodesOnce`：用计数 encoder 或 benchmark `-benchmem` 证明每文档一次 marshal。
- 现有 `TestQueueLimitsAndUnsupportedGuarantees`、`TestQueueAndCloseDrain` 保持通过。

**验收**：每文档一次编码；16 MiB 文档的分配次数与峰值内存明显下降（写入 PR 的 benchmark 数据）。

**实施记录（2026-10-19）**：
- `transport.EncodeChunkFrame` 一次构建并 marshal，返回完整帧；新增 `Client.SendFrame` 负责写帧（含 partial write 循环）；`SendChunk` 改为 `EncodeChunkFrame + SendFrame`；删除无调用方的 `ChunkFrameSize`。
- VSOCK 队列改为保存编码后的 `frame []byte`，`Save` 只编码一次，`send` 直接 `SendFrame`，不再二次编码或复制 `rec.Data`。
- 新增 `internal/toolstream/transport/encode_test.go`：round-trip 解析、partial write、`BenchmarkEncodeChunkFrame`（64 KiB）。
- 更新 `docs/development/toolstream_transport.md`。本机因缺少 capnp 生成物无法运行 transport/vsock 测试，需在 CI 执行 `make gen-build && make check` 并记录 benchmark。

---

### R5. Guest 视图按容器粒度判定完整性

**现象**：一个永久无法绑定的 Guest 容器会让 `snapshot.isComplete = false`，从而永不删除任何容器，并把整个视图标记为 `ErrContainersUnavailable`。

**根因**：
- `internal/pod/guest_scanner.go:131-159`：`readProcess`（ID 未知）与 `bind`（ID 已知、元数据失败）的错误都被并入 `scanErr`，最终 `isComplete = scanErr == nil`。
- `internal/pod/container_events.go:293-300`：只有 `isComplete` 才执行删除。
- `internal/pod/container_events.go:321`：任何 `syncErr` 都覆盖 `v.err`。

**最小修复（复用现有 nil-record 机制）**：
1. `scan` 区分两类错误：
   - **membership 错误**：`readProcess` 失败，进程的容器 ID 未知，可能隐藏容器；仍保留“本次扫描不完整”的语义（`isComplete=false`，不删除，`v.err` 置位）。
   - **binding 错误**：`bind` 失败，容器 ID 已知；写入 `snapshot.records[id] = nil`，不计入 `isComplete`，不返回给 `refresh`。
2. `snapshot.isComplete = membershipErr == nil`，`return snapshot, membershipErr`。
3. `containerStore.commit` 无需新增 `failed` map：现有逻辑已满足
   - `records` 中存在（即使为 nil）→ 不删除；
   - nil record 且无 previous → 不发布；
   - nil record 且有 previous → 保留旧实例（`record == nil` 分支 continue）；
   - `syncErr == nil` → `v.err = nil`，视图保持可用。
4. 绑定错误用 Debug 记录（每次扫描重试，避免刷屏）。进程退出后 ID 从 `grouped` 消失，下一次完整扫描自然删除，无需重试计数或删除策略。

**测试**：
- `TestGuestScannerAmbiguousPath`：歧义路径 → `isComplete=true`、`records[id]=nil`、无 scan error；commit 保留旧实例且 `store.err=nil`；消除歧义后恢复绑定。
- `TestGuestScannerBindingFailureKeepsMembership`：一个容器绑定失败的同时，另一个健康容器退出必须被删除，且视图仍可用。
- 既有 `TestGuestScannerFailureAndReplacement`（`readProcess` 失败）保持“不完整扫描不删除”的语义。

**验收**：单个绑定失败的容器不能冻结删除、不能把视图标记为不可用；进程退出后能被正常清理。

**实施记录（2026-10-19）**：
- `guestScanner.scan` 将 `scanErr` 拆为 `membershipErr`；`bind` 失败只设置 nil record 并 Debug 记录，不再返回错误或影响 `isComplete`。
- `snapshot.records[id] = nil` 现在承担“已知 ID、元数据待定”的显式语义（补充注释）。
- 更新 `TestGuestScannerAmbiguousPath` 并新增 `TestGuestScannerBindingFailureKeepsMembership`。
- 用临时 abi stub（未提交）验证 `internal/pod` 与测试可编译；需在 CI 执行 `go test -race ./internal/pod/...`。

---

### R6. Kata CSS 失败不再致命

**现象**：CPU CSS 不可用（CPU 控制器未启用、事件丢失、probe 未 attach）时，`populateKataContainer` 返回错误；`refresh` 重试 3 次后每次 refresh 都重新返回该错误，导致全节点 `ErrContainersUnavailable`。

**根因**：
- `internal/pod/container_kata.go:55-58` 把可选采样元数据当发现必需项。
- `internal/pod/container_controller.go:310-311` 在 `attempts >= 3` 后永久返回 `pending.err`，而 `retries` 只在 `full` 时清空。

**修复步骤**：
1. `populateKataContainer`：`parseCgroupCPUCSS` 失败时 `log.WithError(...).Debug`（避免每次 refresh 刷屏），设置 `CgroupCss = nil`，继续发布 `InitPid/SandboxID/CgroupPath`。
2. 下个 refresh 会自然重试 CSS；CSS 出现后由 `parseCgroupCPUCSS` 填充，无需额外状态。
3. 检查 CPU 消费方（`core/autotracing`、`core/metrics/cpu_runqlat_tracing.go`）对空 `CgroupCss` 的处理：已有 `css == 0` 跳过逻辑，补齐日志即可。
4. 评估是否需要在 controller 层把“可选字段失败”与“必需字段失败”分开；至少在 `resolveContainerRecord` 返回的 record 上标记降级状态（可选，P2）。

**测试**：
- `TestPopulateKataContainerWithoutCPUCSS`：CSS 缓存为空时容器仍成功发布，`CgroupCss` 为空，`InitPid/SandboxID/CgroupPath` 正确。
- 现有 `TestPopulateKataContainerUsesHostBinding` 保持通过。
- 后续可加“CSS 在第二次 refresh 出现后 record 被更新”的用例（`populateKataContainer` 每次都会重查，行为由现有代码保证）。

**验收**：一个 Kata Pod 的 CSS 缺失不会影响其他容器或整节点视图。

**实施记录（2026-10-19）**：
- `populateKataContainer` 不再因 CSS 失败返回错误；保留 cgroup/PID/sandbox 绑定，`CgroupCss` 为空并在 Debug 级别记录原因。
- 新增 `internal/pod/container_kata_css_test.go`（Linux）：构造 Unified/Legacy/Hybrid cgroup 树与空 CSS 缓存，断言容器发布成功。
- 本机缺少 `internal/bpf/abi`（需 `make gen-build`）；用临时 abi stub（未提交）验证了 `internal/pod` 与新增测试可编译。macOS 无法执行 Linux 测试二进制，需在 CI 运行。

---

### R7. CSS 缓存索引化

**现象**：`knode_len <= 1` 使所有 cgroup（pod/slice/系统）的 mkdir/rmdir 事件都进入缓存；`parseContainerCSS` 对每个容器 `Range` 整张表，refresh 路径为 O(containers × cgroups × subsystems)。

**根因**：
- `bpf/include/bpf_cgroup_css.h:44` 放宽了名称过滤；`internal/pod/container_css_default.go:105-124` 接受空容器 ID 条目。
- `internal/pod/container_css_default.go:92-103` 线性扫描 `sync.Map`。

**修复步骤**：
1. 保留短名 cgroup 的支持（Kata hypervisor cgroup 需要），但把缓存拆成两个索引：
   - `cssByContainer map[string]map[string]uint64`（containerID → subsys → CSS）；
   - `cssByInode map[uint64]map[string]uint64`（inode → subsys → CSS），供 `parseCgroupCPUCSS` O(1) 查询。
   - 保留现有 `sync.Map` 或在初始化时重建索引；写路径同时更新两个索引，删除路径同步清理。
2. `parseContainerCSS` 改为读 `cssByContainer[containerID]`，不再 `Range`。
3. `parseCgroupCPUCSS` 改为读 `cssByInode[inode]`，保留冲突检测（同一 inode 多个 CSS 时报 ambiguous）。
4. 并发安全：使用 `sync.RWMutex` + 两个 map，或继续 `sync.Map` 但按 key 直接 Load；写少读多，RWMutex 更直观。
5. 补 benchmark：`BenchmarkParseContainerCSS`（1k/10k cgroups），对比修改前后。

**测试**：
- 同一容器多子系统 CSS 正确聚合。
- 同一 inode 冲突返回 ambiguous。
- 删除事件后两个索引都不再返回该条目。
- 现有 `TestCgroupCPUCSS`、`TestExtractContainerID` 保持通过。

**验收**：单次 `parseContainerCSS` 不随 cgroup 总数增长；缓存条目随 rmdir 释放。

---

### R8. 复用 Kata runtime binding

**现象**：Kata 容器被排除在“StartedAt 未变则跳过 resolve”的快速路径外，每次 refresh 对每个 Kata 容器顺序调用 `ContainerStatus(..., true)`（5s 超时）。

**根因**：
- `internal/pod/container_controller.go:302` 的 `HostKataSandboxID() == ""` 条件。
- `internal/pod/container_runtime.go:28-35` 每次都走 `readContainerdRuntimeInfo`。

**修复步骤**：
1. 在 `containerRecord` 中缓存 `runtimeType`、`sandboxID`、`initPID`（或复用 `Container` 字段）与 `runtimeVerifiedAt`。
2. Kata 记录走单独的轻量刷新：校验 `/proc/<pid>` 的 `starttime`/`cmdline` 与缓存一致时，只重读 `/proc/<pid>/cgroup` 与 CSS，不调用 CRI。
3. 仅当 PID 不存在、starttime 变化、sandbox 目录变化，或超过验证间隔（如 5 分钟）时才调用 `readContainerdRuntimeInfo`。
4. 把 CRI 调用放到 `ctx` 下并限制并发（例如每轮最多 N 个并发），避免 containerd hang 拖住整个 refresh。

**测试**：
- fake CRI 客户端计数：初始发现 1 次/容器；连续 5 次 refresh 不增加；PID 变化后增加 1 次。
- shutdown 在 CRI 阻塞时能在 ctx 到期后退出（与 R9 的 ctx 传递一起验证）。

**验收**：稳态 refresh 不产生 CRI RPC；Kata Pod 数量增长时 refresh 时间不线性增长。

---

### R9. Sandbox 下从已发布记录解析 PID/cgroup

**现象**：Guest 记录 `RuntimeType == ""`，Sandbox 不初始化容器 provider；`containerInitPIDByID` 落到 `/var/lib/docker` / `/run/containerd` 默认路径，Guest 容器解析失败，`ContainerCgroupPathByID` 不可用。

**根因**：
- `cmd/huatuo-bamai/pod.go:28-35` 只启动 `InitGuestManager`。
- `internal/pod/container_file.go:96-103` 的 provider fallback。
- `internal/pod/container_cgroup_path.go:86-96` 通过 PID 反查 cgroup。

**修复步骤**：
1. `containerInitPIDByID`：先查已发布的 `ContainerByID`，若记录来自 Guest 扫描（`RuntimeType == ""` 且 `CgroupPath` 非空），直接返回 `container.InitPid`，不做运行时 IO。
2. `ContainerCgroupPathByID`：优先使用已发布记录的 `CgroupPath`（并校验 ref 仍是当前实例），失败再走现有 provider 路径。
3. 为 Guest 记录增加显式标识（例如 `Source == guest` 或 `RuntimeType == "kata-guest"`），避免仅凭空字段猜测；同时更新 `HostKataSandboxID()` 的语义注释。
4. 明确 Guest 内 profiler 的可用性：若仍不支持 API-triggered profiling，在文档中说明只能走 standalone profiler。

**测试**：
- Guest 模式下发布一个 record 后，`ContainerCgroupPathByID` 与 `ContainerRootPIDs` 可解析。
- Host 模式行为不变（现有测试通过）。
- 过期 ref 返回 "no longer current"。

**验收**：Guest 内 profiler 可解析容器 cgroup；Host 路径无回归。

---

## P2 详细计划（复核项）

### R1. VSOCK `Close` 返回值不反映已丢弃队列

**复核结论**：原“静默丢失”结论需修正。`Save` 在 `connected == false` 时直接拒绝（`internal/storage/vsock/vsock.go:134-136`），而 `send` 失败时 defer 会清空 `pending` 并打印 `connection lost; dropped N documents`（`vsock.go:196-206`），因此 `run` 顶部的 closed 分支实际上不会遇到非空队列；丢弃路径**有日志**，且 `docs/development/kata_vsock_sender.md` 已声明 “broken connection drops its remaining queue”。

**真实问题（P2）**：
- 连接中断导致的丢弃只写日志，`Close` 仍返回 `nil`，调用方（`closeStores`）无法区分“完整排空”和“有损关闭”。
- 若最后一个 write 在 `Close` 等待期间失败，同样返回 `nil`。

**修复步骤**：
1. 在 `Backend` 记录 `dropped` 计数（复用 `send` defer 中的统计）。
2. `Close` 完成 join 后若 `dropped > 0`，返回 `fmt.Errorf("%w: %d documents", ErrPendingDropped, dropped)`；`ctx.Err()` 优先。
3. 文档补充“Close 可能返回 `ErrPendingDropped`，调用方应视为 best-effort 丢失”。

**测试**：用一个可阻塞/可控失败的 fake conn：握手成功后 `Save` 一个文档触发写入，令 `Write` 返回错误，`send` 的 defer 丢弃该 pending；随后 `Close` 断言 `errors.Is(err, ErrPendingDropped)`。正常排空仍返回 `nil`（现有 `TestQueueAndCloseDrain`）。

**验收**：有损关闭可被调用方识别；正常排空语义不变。

---

### P2 批次

| 批次 | 内容 | 文件 | 建议提交 |
|---|---|---|---|
| B1 正确性加固 | `parseCgroupCPUCSS` 异步 refresh 语义（返回 typed not-ready 或删除 fallback）；v1 常驻 refresh 探针 | `internal/pod/container_cpu_css.go` | `fix(pod): make CPU CSS refresh observable` |
| B1 | Kata memory binding 的 `directory`/空路径语义（sentinel 或置 nil） | `internal/pod/container_cgroup_path.go:213`、`container_events.go:362` | `fix(pod): distinguish unavailable and pending memory bindings` |
| B1 | Guest QoS 未识别时保留 unknown，不默认 Guaranteed | `internal/pod/guest_scanner.go:322-330` | `fix(pod): keep unknown QoS for guest cgroups` |
| B1 | `context.Background()` 改为传入 ctx（CRI + guest bind） | `container_provider.go:147`、`container_cgroup_path.go:241` | `fix(pod): propagate context through runtime and guest validation` |
| B1 | Sandbox 下 `NewRuntime` 错误只忽略预期错误 | `cmd/huatuo-bamai/cgroup.go:48-51` | `fix(cgroup): narrow sandbox self-limit fallback` |
| B2 性能 | `onlinePerfCPUs` 复用 `cpuutil.SystemCPUOnlinePath`，拒绝空列表，限制最大范围 | `internal/bpf/perf_event_pmu.go:111,162-196` | `perf(bpf): parse online CPUs defensively` |
| B2 | Guest 扫描复用 `procfs.FS`、复用 `readProcess` 结果、缓存 namespace-relative 路径 | `internal/pod/guest_scanner.go:172,251-275,311` | `perf(pod): reduce guest scan syscalls` |
| B3 接口/设计 | `DocumentReceiver` 依赖 `TracingSaver`/`ProfilingSaver` 小接口 | `internal/guestbridge/document_receiver.go` | `refactor(guestbridge): depend on saver interfaces` |
| B3 | `vsock.Backend` 拆出 writer 接口，去掉 6 个 unsupported 方法（或保留但注释契约） | `internal/storage/vsock/vsock.go:257-277` | `refactor(storage): narrow vsock backend contract` |
| B3 | `pkg/*/store.Config` 不再暴露 `internal/storage/vsock.Options` | `pkg/tracing/store/store.go:38`、`pkg/profiling/store/store.go:37` | `refactor(store): move vsock options out of public config` |
| B3 | 合并两个 Store 的 VSOCK 构造与互斥校验；统一 `DecodeDocument` 的 trailing 校验 | `pkg/*/store` | `refactor(store): share vsock and decode helpers` |
| B3 | `NewWithSource` 校验 source type；删除无调用的 `NewWithHostname` | `internal/document/builder.go:108-115` | `refactor(document): tighten builder API` |
| B4 风格 | >120 字符行、单行复合字面量、`bridge.go`/`guest_scanner.go` 拆分、`newDocumentBuilder` 归位 | 见审查报告 | `style(*): split long lines and literals` |
| B4 | Copyright：`decode_budget*.go` 的 URL 缩进；integration test 的 build tag 与头顺序统一 | `internal/toolstream/transport/decode_budget*.go`、`internal/guestbridge/*_test.go` | `style(*): normalize license headers` |
| B4 | 重复 package doc、`CGROUP_KNODE_NAME_MINLEN` 死宏、`"cpu"` 字面量、`[:12]` 魔数、三个 64-hex 正则 | 见审查报告 | `style(*): remove duplicated constants and comments` |
| B4 | Cilium 过期注释、`cgroup_types.h` 空格缩进 | `internal/bpf/perf_event_online_test.go:15`、`bpf/include/abi/cgroup_types.h:26` | `style(*): fix comments and indentation` |
| B5 文档 | 9 个新文档补 frontmatter；3 篇 development 文档补 `_en/_zh` 配对；修复 zh/en 链接漂移 | `docs/deployment/kata_*`、`docs/development/kata_*` | `docs(kata): add frontmatter and zh counterparts` |
| B5 | `Mode`/GuestTransport/GuestBridge 配置示例与默认值一致性检查 | `huatuo-bamai.conf`、`huatuo-bamai-sandbox.conf`、docs | `docs(kata): align configuration examples` |
| B6 测试 | 超短真实时钟改 `require.Eventually`/注入时钟；`<-result` 加超时；`_ = Save` 检查错误 | `internal/guestbridge/*_test.go`、`vsock_test.go`、`transport/*_test.go` | `test(*): remove timing flakes` |
| B6 | 补覆盖：`ServeWithOptions` 校验、协议版本拒绝、`frame > budget`、`New` 错误分支、`ContainerByCSS`/representative 边界、`hasGuestAgentIdentity` 变体 | 见审查报告 | `test(*): cover missing branches` |
| B6 | integration tag 过重：httptest/loopback 测试转为普通单测 | `profile_ingest_integration_test.go`、`transport_integration_test.go` | `test(*): move hermetic tests out of integration` |

---

## 执行顺序与依赖

```
Wave 0（立即）
  R3 删除未使用常量 → 跑通 make gen-build && make check 基线

Wave 1（安全/可用性）
  R2 socket 清理复用 removeSocketIfSame

Wave 2（正确性/视图稳定性）
  R6 Kata CSS 非致命（小改动，先降低故障面）
  R5 Guest 视图 per-ID 完整性（结构性改动，依赖 R6 的测试场景）
  R9 Sandbox PID/cgroup 解析

Wave 3（性能）
  R4 单次编码
  R7 CSS 索引化（依赖 R6 的 CSS 语义）
  R8 Kata runtime binding 缓存（依赖 R9 的 ctx 传递）

Wave 4（P2）
  R1 vsock Close 有损关闭可识别
  B1 → B2 → B3 → B4 → B5 → B6
```

依赖说明：

- R5 与 R6 都涉及 `ErrContainersUnavailable` 语义，建议同一迭代完成；先 R6 可快速止血，再由 R5 做结构性修复。
- R7 依赖 BPF 侧过滤策略；若暂不改 C 侧，可先做 Go 侧双索引，C 侧过滤作为后续优化。
- R8 依赖 R9 的 ctx 传递（否则 shutdown 仍可能被 CRI 阻塞）。
- B1 的 ctx 传递与 R8/R9 有重叠，可在同一 PR 中先完成 ctx 改造。

---

## 验证矩阵

每个 PR 至少执行：

```sh
make gen-build          # 需要 capnp / clang / bpf2go；生成 internal/bpf/abi、event.capnp.go、mock
make check              # openapi + generate + format + golangci-lint + git diff
make unit
```

受影响包额外执行：

```sh
go test -race ./internal/guestbridge/...
go test -race ./internal/storage/vsock/...
go test -race ./internal/pod/...
go test -race ./internal/toolstream/transport/...
go test -race ./core/autotracing/...
```

集成/端到端（按需）：

```sh
make integration
make e2e
```

Kata 真机验证（对应 R2/R6/R7/R8/R9）：

- QEMU：Guest 连接 → 认证 → tracing 文档落盘；共享 CPU CSS 归属唯一。
- Firecracker：长 socket 路径、关闭重绑、对端 PID 校验。
- Guest 断线/重连：R1 的丢弃日志与错误可观测。
- Kata Pod 缺 CPU 控制器：R6 下容器仍可见，其他容器不受影响。

---

## 提交与验收规范

- 每个 R 项一个 commit（或一个 PR），subject 使用 `fix|perf|refactor|test|docs(scope): summary`，body 说明 why，行宽 ≤80，无 `Co-authored-by`。
- 每个 commit 必须包含对应测试；无法单测的（BPF/真机）在 PR 描述中给出验证命令与结果。
- 不修改生成文件；如需变更 BPF ABI，先跑 `make gen-build` 并提交生成物。
- 合并前在 PR 描述中附：`make check` 输出、`make unit` 输出、受影响包的 `-race` 输出。

## 风险与回滚

| 项 | 风险 | 回滚 |
|---|---|---|
| R5 | 绑定失败不再让视图不可用，订阅者会看到“旧 record + 可用视图” | 语义更精确；`ValidateContainerRef` 仍会拒绝过期绑定，必要时回退为 `isComplete=false` |
| R7 | 双索引与现有 `sync.Map` 写入路径并发 | 单测 + race；保留旧 `cgroupListCssDataByKnode` 作为回退路径 |
| R8 | 缓存 runtime binding 可能错过沙箱替换 | 校验 `/proc/<pid>` starttime + 定期强校验；异常时回退 CRI |
| R4 | 编码方式改变可能影响 wire 兼容 | 编码结果必须能被现有 `messageDecoder` 解析；协议版本不变 |
| R2 | 删除路径清理逻辑可能遗漏清理 | 保留 listener 自清理 + 测试覆盖正常关闭/异常关闭 |

---

## 跟踪表

| ID | 状态 | Owner | PR | 备注 |
|---|---|---|---|---|
| R1 | TODO | | | |
| R2 | DONE | | 本次改动 | socket listener 记录 bind 时的 `FileInfo`，统一走 `fileutil.RemoveIfSame`；新增替换 socket 回归测试 |
| R3 | DONE | | 本次改动 | 删除 `guestPaymentID`；CI 跑 `make gen-build && make check` 待确认 |
| R4 | DONE | | 本次改动 | `EncodeChunkFrame` + `SendFrame`；VSOCK 队列保存帧；CI 跑 `make check` 与 benchmark 待确认 |
| R5 | DONE | | 本次改动 | 复用 nil-record 机制；`bind` 失败不影响 membership；新增/更新测试；CI 跑 `make check` 待确认 |
| R6 | DONE | | 本次改动 | CSS 缺失不再致命；新增 `container_kata_css_test.go`；CI 跑 `make check` 待确认 |
| R7 | TODO | | | |
| R8 | TODO | | | |
| R9 | TODO | | | |
| B1–B6 | TODO | | | 按批次拆 PR |

## 不在本计划内（后续评估）

- 为 GuestBridge 增加独立的 `FrameReadTimeoutSeconds` 配置（当前预算解码默认 10s，16 MiB 帧需 >1.6 MB/s；见审查报告第 12 节）。
- OpenAPI `source_type` 改为 enum（当前为自由字符串，保持前向兼容）。
- `!didi` build tag 的仓库级改名（需单独评估内部构建流程）。
- Guest API-triggered profiling 的 publication/visibility 支持（当前因 `ResultPublisher == nil` 被拒绝，standalone profiler 才可转发）。

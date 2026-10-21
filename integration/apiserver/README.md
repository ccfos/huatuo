# apiserver 测试

对应测试文档 6.3。F02/F03/F04/F08/F13 和 M01–M19 由 `make e2e`
自动发现；其余 8 项功能通过 `integration/test_apiserver_profiling.sh` 接入
`make integration`。功能用例也可以通过本目录的 `run.sh` 独立执行。
服务配置、请求和结果断言共用 `lib.sh`，
火焰图 protobuf 校验共用 `probe.go`。

在专用 Linux 测试节点执行，先运行 `make build`。需要 root、Docker、
Go（与 go.mod 一致）、Python 3、gcc、curl、jq、ss、unshare、flock，
以及可用的 BPF/perf 环境。独立入口拒绝与已运行的 huatuo-bamai 共存，
因为其 Toolstream socket 路径固定；不会停止已有服务。e2e 用例会先停止
runner 在该用例工作目录启动的默认 bamai，再使用测试专用配置启动服务。

```bash
# 标准集成测试入口：剩余 8 项功能
sudo bash integration/run.sh test_apiserver_profiling.sh
# 独立执行并保留报告
sudo bash integration/apiserver/run.sh
sudo bash integration/apiserver/run.sh --help
```

独立入口自动启动独立 ES 容器、bamai、apiserver 和 CPU 负载，
验证能力表、无结果存储下的静态 API、两级容量限制、非法参数、
12 小时合法时长（创建后立即停止）、不存在容器、节点不可达、
bamai 启动前不可达。所有等待均有截止时间。

独立入口退出码：0 全部通过，1 断言失败，77 前置条件不足，2 参数错误。
不要把 77 当作通过。独立入口显示报告目录，成功和失败均保留
`summary.tsv`、请求/响应、采样结果及服务日志；不会在 Cooper 文档中标记通过。
标准 runner 将这 8 项作为一个脚本汇总，成功或跳过后删除工作目录，失败时保留。
需要保留成功报告时使用上述独立入口。

用 `APISERVER_TEST_OUTPUT` 指定一个尚未使用、空间充足的报告目录。
ES 数据绑定到其中的 `elasticsearch-data/`，避免 Docker 系统盘水位影响测试。
生成的配置含测试 token；报告仅供测试环境使用。

## e2e：完整任务链路与多语言矩阵

```bash
# F02：创建、完成、list/raw 分页、火焰图及 agent 指标
sudo bash e2e/run.sh test_apiserver_profiling.sh
# F03/F04/F08/F13：停止、并发、apiserver 恢复、Node 故障恢复
sudo bash e2e/run.sh test_apiserver_profiling_stop.sh
sudo bash e2e/run.sh test_apiserver_profiling_concurrent.sh
sudo bash e2e/run.sh test_apiserver_profiling_recovery.sh
sudo bash e2e/run.sh test_apiserver_profiling_node_recovery.sh
# M01–M19：真实 Kubernetes 容器采样
sudo env APISERVER_TEST_NODE=your-local-node \
  KUBELET_CERT=/etc/kubernetes/pki/apiserver-kubelet-client.crt \
  KUBELET_KEY=/etc/kubernetes/pki/apiserver-kubelet-client.key \
  bash e2e/run.sh test_apiserver_profiling_matrix.sh
# 全部 e2e 用例，包含上述脚本
sudo env APISERVER_TEST_NODE=your-local-node make e2e
```

矩阵必须在所选 Kubernetes 节点本机运行，脚本比对 InternalIP 和本机地址。
需要 kubectl 管理权限、可访问本机 10250 的凭证和已打包的 asprof/py-spy。
临时 namespace、ConfigMap、Pod 自动创建，退出时删除。
每次只运行一个负载 Pod，依次验证 C/C++/Go 的 oncpu、offcpu、三种内存模式，
Java CPU/两种内存模式及 Python CPU。Python 的采集窗口是整个任务时长，
不能要求其返回多个窗口。结果必须非空、容器归属正确，火焰图 protobuf
必须能解码且有正采样值。此处不验证 Grafana 页面实际渲染。
C/C++/Go 负载循环保留 16 个 1 MiB 映射，避免分配后立即全部释放，
使 `physical_usage` 在最终快照时仍有可采样的存活内存。

默认镜像 gcc:14、golang:1.24、eclipse-temurin:17-jdk、python:3.11-slim。
可用 `APISERVER_TEST_NATIVE_IMAGE`、`APISERVER_TEST_GO_IMAGE`、
`APISERVER_TEST_JAVA_IMAGE`、`APISERVER_TEST_PYTHON_IMAGE` 替换为内网镜像。
环境不支持某采样模式时会明确失败，不能视为完成整个矩阵。
e2e 报告位于 runner 为每个脚本
创建并保留的工作目录，不使用 `APISERVER_TEST_OUTPUT`；矩阵的 `summary.tsv`
逐项记录 19 个组合，runner 将整个矩阵脚本计为一个用例。
Node 故障恢复用例会主动 SIGKILL 测试 Node，断言原任务 failed/node_unavailable，
并验证恢复后的新任务、raw 和火焰图。故障期间允许预期错误日志，仍拒绝 runtime
panic；runner 继续检查恢复后 Node 的日志。

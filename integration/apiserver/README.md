# apiserver profiling 测试

测试通过已构建的 apiserver、Node 和存储验证任务限制、参数校验、采样结果及故障恢复。
标准入口为 `make integration` 和 `make e2e`。直接运行脚本前执行：

```bash
make build test-build
```

## 集成测试

```bash
# 能力表、认证、无存储 API：无需 Docker、perf 或编译器
sudo bash integration/run.sh test_apiserver_profile_disabled.sh
# 主机/全局配额、Node 容量、非法请求、容器不存在、主机不可达、Node 离线
sudo bash integration/run.sh test_apiserver_profiling.sh
# 独立运行上述 7 项采样功能检查，保留成功和失败报告
sudo bash integration/apiserver/run.sh
```

采样测试需要 Linux、root、Docker、Python 3、gcc、curl、jq、ss、pgrep，
以及已构建的 BPF 和 profiler 工具；`perf_event_paranoid` 必须允许采样。
缺少依赖返回 77；断言失败返回 1。标准 runner 失败时保留工作目录。
独立入口使用 `APISERVER_TEST_OUTPUT` 指定尚未使用的报告目录，默认新建临时目录。
ES 数据绑定到报告目录下的 `elasticsearch-data/`，避免占用 Docker 的可写层。
报告包含 `summary.tsv`、请求响应、采样结果和日志，结果记录为 PASS/FAIL；
依赖不足会单独标记 SKIP。生成配置含测试 token，仅供测试环境使用。
独立入口不会停止已有的 Node，运行前须自行停掉冲突服务。

## e2e 测试

```bash
# 全部 apiserver 场景；每个场景独立清理和统计
sudo bash e2e/test_apiserver.sh
# 与标准 runner 的测试组入口等价
sudo bash e2e/run.sh test_apiserver.sh
# 单独验证任务完成、list/raw 分页、火焰图和 agent 指标
sudo bash e2e/run.sh apiserver/test_apiserver_profiling.sh
# 单独验证 Kubernetes 多语言矩阵
sudo bash e2e/run.sh apiserver/test_apiserver_profiling_matrix.sh
# 全部 e2e，包含上述场景
sudo make e2e
```

`e2e/apiserver/` 包含任务完成、停止、同机并发、apiserver 重启恢复、
Node 故障恢复和多语言矩阵六个场景。runner 分别启动进程、准备工作目录、
恢复默认 Node、检查日志并记录结果；失败立即结束，所有 e2e 报告均保留。
矩阵的 `summary.tsv` 另行记录 19 个语言/模式组合。

矩阵需要 kubectl 管理权限、crictl、本机 kubelet 凭证和已打包的 asprof/py-spy。
未设置 `APISERVER_TEST_NODE` 时，通过节点 InternalIP 自动匹配本机节点；
显式指定时同样验证本机归属。凭证可通过 `KUBELET_CERT`、`KUBELET_KEY` 指定。
无法唯一匹配节点时失败，不能视为完成矩阵。

矩阵先通过本机 CRI 集中准备镜像，再以 `imagePullPolicy: Never` 启动 Pod。
默认镜像 gcc:14、golang:1.24、eclipse-temurin:17-jdk、python:3.11-slim。
可用 `APISERVER_TEST_NATIVE_IMAGE`、`APISERVER_TEST_GO_IMAGE`、
`APISERVER_TEST_JAVA_IMAGE`、`APISERVER_TEST_PYTHON_IMAGE` 替换为内网镜像。
下载失败保留独立日志并报告失败。

每次只运行一个负载 Pod，依次验证 C/C++/Go 的 oncpu、offcpu、三种内存模式，
Java CPU/两种内存模式及 Python CPU，共 19 个组合。
Go Pod 只读挂载 `_output/tests/` 中预编译的静态负载，不在运行时编译 Go 源码。
源码夹具集中在 `integration/testdata/`；C/C++ 和 Java 在对应运行时镜像中准备负载。
C/C++/Go 负载保留 16 个 64 KiB 映射，使最终快照仍有存活内存。

测试检查结果非空、类型正确、容器归属正确，火焰图可解码且采样值为正。
Python 采集窗口为整个任务时长，不要求多个窗口。
临时 namespace、ConfigMap、Pod 在退出时清理；不验证 Grafana 页面实际渲染。
Node 故障恢复会 SIGKILL 测试 Node，验证原任务失败及恢复后新任务和结果可用。
故障期间允许预期错误日志，仍拒绝 runtime panic。

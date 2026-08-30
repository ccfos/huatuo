# IOCOST 集成测试

## 准备

在仓库根目录构建测试产物：

```bash
make oetest-build oetest-manifest
```

如在其他机器构建，将 `_output/oetest/`、`bpf/iocost_tracing.o` 和 `_output/test-bpf/iocost_tracing_test.o` 复制到测试机同一提交的仓库对应目录。

测试机需为非 PREEMPT_RT Linux x86_64，具备 IOCOST、BPF/BTF 和 kprobe 能力，能读取内核配置，并在 cgroup v2 根层级启用并委派 `io` controller。`lifecycle` 模式还需要 `scsi_debug` 模块。

## 运行

在测试机仓库根目录通过统一入口执行，默认运行 `functional`：

```bash
sudo env TEST_IOCOST_REQUIRED=1 bash integration/run.sh test_iocost.sh
```

通过 `TEST_IOCOST_MODE` 选择其他模式，例如：

```bash
sudo env TEST_IOCOST_REQUIRED=1 TEST_IOCOST_MODE=pressure bash integration/run.sh test_iocost.sh
```

也可通过独立入口选择模式：

```bash
sudo env TEST_IOCOST_REQUIRED=1 bash integration/iocost/run.sh functional
```

退出码为 `0` 表示通过。请在专用测试机运行，测试会创建 loop 设备和 cgroup，并修改测试设备的 IO 配置；正常结束后自动清理。

## 测试内容

| 模式 | 覆盖内容 |
| --- | --- |
| `functional` | 等待数量、平均等待时间、重复唤醒、跨采集周期等待及 cgroup offline |
| `lifecycle` | cgroup 和设备删除、设备重绑，以及新对象和其他设备的统计 |
| `faults` | map 容量耗尽、残留等待记录覆盖和删除失败 |
| `pressure` | 32 并发无等待 IO、64 个新 cgroup 串行短等待、单次长等待、多设备和设备重建 |
| `compat` | 内核符号、结构字段、探针挂载和实际等待 |
| `all` | 依次执行以上五种模式 |

## 参数

| 参数 | 说明 |
| --- | --- |
| `run.sh` 最后的模式名 | 选择上表中的测试模式 |
| `TEST_IOCOST_MODE` | 统一入口的测试模式，默认 `functional` |
| `TEST_IOCOST_REQUIRED` | 必须为 `1`；环境不满足或必需用例跳过时，测试失败 |

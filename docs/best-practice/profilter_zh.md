---
title: 性能剖析
type: docs
description: ""
author: HUATUO Team
date: 2026-06-18
weight: 4
---

## 火焰图格式

在性能剖析领域，**collapsed** 和 **flamegraph** 是最常用的两种火焰图格式，分别对应"原始数据"与"可视化视图"两个层次。

### Collapsed 格式

#### 标准语法与格式

collapsed 格式（又称 folded stacks）由 Brendan Gregg 定义，是火焰图的**原始文本输入格式**。每行代表一条唯一的调用栈及其采样计数。

**基本规则：**

```text
frame1;frame2;frame3;...;frameN COUNT
```

| 组成部分 | 说明 |
|---------|------|
| `frame1` | 栈底（入口/根帧），如 `main`、`start_thread` |
| `;` | 帧分隔符（分号） |
| `frameN` | 栈顶（当前执行帧，即采样命中点） |
| `COUNT` | 采样次数（整数），与栈帧之间用**空格**分隔 |

**格式要点：**

- 每行一条独立调用栈，相同栈路径的样本合并计数
- 帧的排列顺序：从左到右为 **根→叶**（调用链方向）
- 空行及 `#` 开头的行通常被视为注释，解析时忽略
- COUNT 的语义取决于分析模式：CPU 采样时为采样次数，内存分配时为分配字节数，锁分析时为竞争时间（毫秒）

**扩展规范：**

部分剖析工具（如 async-profiler）在标准格式基础上引入了**帧类型注解**，用于标识帧的运行时类别：

```text
frameName_{type} COUNT
```

| 注解 | 含义 | 说明 |
|------|------|------|
| `_[j]` | JIT compiled Java | JIT 编译后的 Java 方法 |
| `_[i]` | Interpreted Java | 解释执行的 Java 方法 |
| `_[k]` | Kernel | 内核态帧 |
| `_[n]` | Native C/C++ | 原生 C/C++ 帧 |
| `_[t]` | Thread | 线程帧 |

此外，部分工具支持**带权重的折叠格式**（weighted collapsed），用于差分火焰图：

```text
frame1;frame2;frameN WEIGHT
```

其中 `WEIGHT` 为浮点数，表示该栈的权重值而非简单计数。

#### 样本示例

**CPU 分析示例（以下数据源自 async-profiler 官方文档）：**

```text
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult 21
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeInt 1
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeInt;java/io/ByteArrayOutputStream.write 5
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF 12
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF;java/lang/String.length 3
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.write 6
start_thread;thread_native_entry;Thread::call_run;VMThread::run;VMThread::inner_execute;VMThread::evaluate_operation;VM_Operation::evaluate;VM_GenCollectForAllocation::doit;GenCollectedHeap::satisfy_failed_allocation;GenCollectedHeap::do_collection;GenCollectedHeap::collect_generation;DefNewGeneration::collect;DefNewGeneration::FastEvacuateFollowersClosure::do_void 12
```

**带帧类型注解的示例（async-profiler 扩展）：**

```text
Main.run_[j];Service.process_[j];DAO.query_[j];mysql_real_query_[n] 45
Main.run_[j];Service.process_[j];DAO.query_[j];recv_[k] 18
```

#### 核心用途

| 用途 | 说明 |
|------|------|
| **火焰图生成** | 作为 `flamegraph.pl`、`inferno` 等可视化工具的标准输入格式 |
| **差分分析** | 对比两次 collapsed 文件，生成红蓝差分火焰图，定位性能回归 |
| **程序化处理** | 纯文本格式，便于用 `awk`、`sed`、Python 等工具做自定义聚合与过滤 |
| **跨工具互操作** | Brendan Gregg 定义的通用标准，几乎所有火焰图工具链都支持此格式 |
| **长期存储** | 文本格式体积小，适合归档和版本对比 |
| **CI/CD 集成** | 可在流水线中自动采集、diff、判断性能回归阈值 |

**生成命令示例：**

```bash
# 以 async-profiler 为例
asprof -d 30 -f profile.collapsed -o collapsed <PID>
```

---

### Flamegraph 格式

#### 标准语法与格式

flamegraph 格式是一个**自包含的 HTML 文件**，内嵌 SVG 可视化与 JavaScript 交互逻辑，可直接在浏览器中打开。

**结构组成：**

```bash
flamegraph.html
├── HTML 骨架 + CSS 样式
├── SVG 火焰图主体
│   ├── <g> 每个帧对应的矩形块
│   │   ├── <title> 帧名称 + 采样数/占比
│   │   └── <rect> 位置、宽高、颜色
│   └── ...
├── JavaScript 交互逻辑
│   ├── 点击缩放（zoom into subtree）
│   ├── 搜索高亮（search & highlight）
│   ├── 悬浮提示（tooltip）
│   └── 重置视图（reset zoom）
└── 元数据（title、total samples 等）
```

**视觉编码规则：**

| 维度 | 编码含义 |
|------|---------|
| **X 轴** | 调用栈帧按字母序排列（**非时间线**），宽度与采样数成正比 |
| **Y 轴** | 调用栈深度，底部为根帧，顶部为叶帧 |
| **帧宽度** | 该帧在栈中出现的采样比例，越宽表示消耗资源越多 |
| **帧颜色** | 标识帧类型（见下表） |

**帧颜色规范（以 async-profiler 为参考）：**

> **注意**：火焰图的颜色方案并非跨工具统一标准。Brendan Gregg 原始 `flamegraph.pl` 使用随机暖色调，颜色无语义含义；`perf`/`bpftrace` 通常按 DSO 着色或使用随机色；async-profiler 则按帧类型语义着色。以下为 async-profiler 的配色规范：

| 颜色 | 帧类型 | 说明 |
|------|--------|------|
| 🟢 绿色 | Java (interpreted) | 解释执行的 Java 方法 |
| 🟡 黄/橙色 | Java (JIT compiled) | JIT 编译后的 Java 方法 |
| 🔴 红色 | C/C++ (native) | 原生 C/C++ 代码 |
| 🔵 蓝色 | Kernel | 内核态代码 |
| ⬜ 灰色 | Other/Unknown | 其他类型或未知帧 |

**扩展特性（以 async-profiler 为参考）：**

- **Icicle Graph（冰柱图）**：自顶向下展示调用链（根在顶部），更符合自上而下的阅读习惯，通过 `--reverse` 选项或浏览器内 Reverse 按钮切换
- **多线程视图**：不同线程的调用栈并列展示在根级别
- **搜索高亮**：输入关键词后，匹配帧高亮为紫色，不匹配帧变暗
- **采样信息提示**：悬浮显示帧名、采样数、占总采样百分比
- **Cutoff 帧**：标记为 `[...]` 的帧表示栈截断（如因栈深度限制）

#### 样本示例

**生成命令示例：**

```bash
# 以 async-profiler 为例
asprof -d 30 -f flamegraph.html <PID>
```

**交互操作：**

- **点击帧**：缩放至该帧为全宽，仅展示其子树
- **搜索框**：输入关键词，匹配帧高亮
- **悬浮**：显示帧名、采样数、百分比
- **Reset Zoom**：恢复全局视图

#### 核心用途

| 用途 | 说明 |
|------|------|
| **热点定位** | 直观识别最宽的帧块，快速找到 CPU/内存消耗最大的代码路径 |
| **根因分析** | 从叶帧向上追溯，理解资源消耗的调用链上下文 |
| **团队协作** | HTML 文件可直接分享，无需安装额外工具，浏览器即可查看 |
| **性能优化验证** | 优化前后各生成一张火焰图，对比帧宽度变化验证优化效果 |
| **非专业友好** | 可视化形式对非性能工程师也更易理解，便于跨团队沟通 |

---

### 两种格式对比

| 对比维度 | Collapsed | Flamegraph |
|---------|-----------|------------|
| 格式类型 | 纯文本 | HTML + SVG |
| 人可读性 | 中等（需理解栈帧语法） | 高（可视化，直觉理解） |
| 机器可读性 | 高（易解析、易 diff） | 低（需解析 HTML/SVG） |
| 交互性 | 无 | 支持缩放、搜索、悬浮提示 |
| 文件大小 | 极小（KB 级） | 较大（百 KB~MB 级） |
| 工具链依赖 | 无（纯文本） | 浏览器 |
| 差分分析 | 原生支持（diff 两个文件） | 需转换为 collapsed 后 diff |
| 典型使用场景 | 程序化处理、CI 对比、存档 | 人工分析、团队分享、演示 |

**典型工作流：**

```bash
采集 ──► collapsed ──► flamegraph.html（人工分析）
                  │
                  ├──► 差分火焰图（性能回归检测）
                  ├──► 自定义聚合脚本
                  └──► 归档存储
```

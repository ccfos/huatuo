简体中文 | [English](./README_EN.md)

![](./docs/huatuo-logo-v3.png)

# 什么是 HUATUO
**HUATUO（华佗）**是由**滴滴**开源并依托 **CCF 开源发展委员会**孵化的云原生操作系统可观测性项目，专注于为复杂云原生环境提供操作系统内核级深度观测能力。该项目基于 [eBPF](https://docs.kernel.org/userspace-api/ebpf/syscall.html) 技术，通过整合 [kprobe](https://www.kernel.org/doc/html/latest/trace/kprobes.html)、 [tracepoint](https://www.kernel.org/doc/html/latest/trace/tracepoints.html)、 [ftrace](https://www.kernel.org/doc/html/latest/trace/ftrace.html)  等内核动态追踪技术，实现了多维度的内核观测能力：**1.** 更精细化的内核子系统埋点指标 Metric **2.** 异常事件驱动的内核运行时上下文捕获 Events **3.** 针对系统突发毛刺的自动追踪 AutoTracing、AutoProfiling。该项目逐步构建了完整的 Linux 内核深度可观测体系架构。目前，HUATUO 已在滴滴生产环境中实现规模化部署，在诸多故障场景中发挥关键作用，有效保障了云原生操作系统的高可用性和性能优化。通过持续的技术演进，希望 HUATUO 能够推动 eBPF 技术在云原生可观测领域向更细粒度、更低开销、更高时效性的方向发展。更多信息访问官网 [https://huatuo.tech](https://huatuo.tech/)。


# 核心特性
- **低损耗内核全景观测**：基于 BPF 技术，保持性能损耗小于1%的基准水位，实现对内存管理、CPU 调度、网络及块 IO 子系统等核心模块的精细化、全维度、全景观测与性能剖析。通过自适应采样机制，实现系统资源损耗与观测精度的动态平衡。
- **异常事件驱动诊断**：构建基于异常事件驱动的运行时上下文捕获机制，聚焦内核异常与慢速路径的精准埋点。当发生缺页异常、调度延迟、锁竞争等关键事件时，自动触发调用链追踪，生成包含寄存器状态、堆栈轨迹及资源占用的图谱诊断信息。
- **全自动化追踪 AutoTracing**：AutoTracing 模块采用启发式追踪算法，解决云原生复杂场景下的典型性能毛刺故障。针对 CPU idle 掉底，CPU sys 突增，IO 突增，loadavg 突增等棘手问题，实现自动化快照留存机制和根因分析。
- **持续性能剖析 Profiling**：持续对操作系统内核，应用程序进行全方位性能剖析，涉及系统 CPU、内存、I/O、 锁、以及各种解释性编程语言，力助业务持续的优化迭代更新。该功能在哨兵压测，防火演练，重要节假日护堤等场景发挥关键作用。
- **分布式链路追踪 Tracing**：以网络为中心的面向服务请求的分布式链路追踪，能够清晰的划分系统调用层级关系，节点关联关系，耗时记账等，支持在大规模分布式系统中的跨节点追踪，提供微服务调用的全景视图，保障系统在复杂场景下的稳定性。
- **开源技术生态融合**：无缝对接主流开源可观测技术栈，如 Prometheus、Grafana、Pyroscope、Elasticsearch等。支持独立物理机和云原生部署，自动感知 K8S 容器资源/标签/注解，自动关联操作系统内核事件指标，消除数据孤岛。通过零侵扰、内核可编程方式兼容主流硬件平台和内核版本，确保其适应性、应用性。

# 软件架构
![](./docs/architecture.svg)

# 快速上手

- **极速体验**
如果你只关心底层原理，不关心存储、前端展示等，我们提供了编译好的镜像，已包含 HUATUO 底层运行的必要组件，直接运行即可：
    ```bash
    $ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /run:/run huatuo/huatuo-bamai:latest
    ```

- **快速搭建**
如果你想更进一步了解 HUATUO 运行机制，架构设计等，可在本地很方便地搭建 HUATUO 完整运行的所有组件，我们提供容器镜像以及简单配置，方便用户开发者快速了解 HUATUO。
    ![](./docs/quick-start-cn.png)
    <div style="text-align: center; margin: 8px 0 20px 0; color: #777;">
    <small>
    HUATUO 组件运行示意图<br>
    </small>
    </div>

    为快速搭建运行环境，我们提供一键运行的方式，该命令会启动 [elasticsearch](https://www.elastic.co), [prometheus](https://prometheus.io), [grafana](https://grafana.com) 以及 huatuo-bamai 组件。命令执行成功后，打开浏览器访问 [http://localhost:3000](http://localhost:3000) 即可浏览监控大盘。

    ```bash
    $ docker compose --project-directory ./build/docker up
    ```

# 内核版本
理论支持 4.18 之后的所有版本，主要测试内核、和操作系统发行版如下：

|  HUATUO      |  内核版本 |  操作系统发行版     |
| :---  |    :----  |  :--- |
| 1.0      | 4.18.x      | Centos 8.x   |
| 1.0      | 5.10.x      | OpenEuler 22.03/Anolis OS 8.10  |
| 1.0      | 6.6.x       | OpenEuler 24.03/Anolis OS 23.3 |
| 1.0      | 6.8.x       | Ubuntu 24.04 |
| 1.0      | 6.14.x      | Fedora 42 |


# 文档

更多信息访问官网 [https://huatuo.tech](https://huatuo.tech/)


# 联系我们

@[hao022](https://github.com/hao022)  
@[nashuiliang](https://github.com/nashuiliang)  
@[fanzu8](https://github.com/fanzuba)  

# 开源协议
该项目采用 Apache License 2.0 协议开源，BPF 代码采用 GPL 协议。
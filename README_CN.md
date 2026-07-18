<p align="center">
  <img src="docs/img/huatuo-logo-v4.png" alt="Cube Sandbox Logo" width="140" />
</p>

<h1 align="center">HUATUO 华佗</h1>

<p align="center">
  <strong>Kernel-wide Insight, Instant Observability, AutoTracing, Continuous Profiling</strong>
</p>

<p align="center">
  <a href="https://github.com/ccfos/huatuo/stargazers"><img src="https://img.shields.io/github/stars/ccfos/huatuo?style=social" alt="GitHub Stars" /></a>
  <a href="https://hub.docker.com/u/huatuo"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/huatuo/huatuo-bamai?style=social"/></a>
  <a href="https://github.com/ccfos/huatuo/issues"><img src="https://img.shields.io/github/issues/ccfos/huatuo" alt="GitHub Issues" /></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-green" alt="Apache 2.0 License" /></a>
  <a href="./CONTRIBUTING.md"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen" alt="PRs Welcome" /></a>
  <a href="https://landscape.cncf.io/?item=observability-and-analysis--observability--huatuo"><img src="https://img.shields.io/badge/CNCF-Landscape-0C66E4" alt="CNCF Landscape" /></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/⚡_eBPF-Zero_Instrumentation-blue" alt="Fast startup" />
  <img src="https://img.shields.io/badge/🔒_Observability-Agent_Sandbox,_Linux_Kernel,_Hardware_Level-critical" alt="Hardware-level observability" />
  <img src="https://img.shields.io/badge/📦_Deploy-Large_Scale-orange" alt="large scale" />
</p>

<p align="center">
  <a href="./README.md"><strong>English README</strong></a> ·
  <a href="https://docs.huatuo.tech/en/latest/quick-start/"><strong>Quick Start</strong></a> ·
  <a href="https://docs.huatuo.tech/"><strong>Documentation</strong></a> ·
</p>

---

## 什么是 HUATUO

**HUATUO（华佗）** 是由滴滴开源并依托中国计算机学会（CCF）孵化的操作系统内核深度观测项目。它应用于通用计算、AI 计算、智驾系统以及裸金属基础服务等场景。
通过整合 **kprobe**、**tracepoint**、**ftrace** 和 **eBPF** 等 Linux 内核动态追踪技术，HUATUO 提供了多维度的内核洞察力：更精细化的指标、异常事件驱动的内核运行时上下文捕获，以及智能化的自动追踪。项目已构建起一套完整的 Linux 内核深度可观测体系架构。
HUATUO 已在滴滴生产环境中实现规模化部署，在诸多故障场景中发挥作用，有效保障了云原生操作系统的高可用性和性能优化。通过持续演进，HUATUO 旨在将 eBPF 可观测性技术推向更细粒度、更低开销和更高效率。

HUATUO 已进入 [CNCF Landscape](https://landscape.cncf.io/?item=observability-and-analysis--observability--huatuo).

## 核心特性

- **低损耗内核全景观测**：基于 BPF 技术，保持性能损耗低于 1%，实现对内存管理、CPU 调度、网络及块 I/O 等核心子系统的全栈、全维度、全景观测。
- **异常事件驱动诊断**：构建事件驱动的运行时上下文捕获机制，精准埋点内核慢速、异常路径。当发生缺页异常、调度延迟、锁竞争等关键事件时自动触发，即刻生成包含寄存器状态、调用堆栈、资源占用等详细诊断信息。
- **全自动化追踪 (AutoTracing)**：采用启发式追踪算法，解决云原生复杂环境下典型的性能毛刺故障。针对 CPU idle 掉底、CPU sys 突增、I/O 突增、Loadavg 突增等棘手问题，实现自动化快照留存与根因诊断。
- **持续性能剖析 Profiling**：持续对操作系统内核，业务应用进行全方位性能剖析，涉及 CPU、内存、I/O、 锁、以及各种解释性编程语言，力助业务持续的优化迭代更新。在全链路压测、故障注入、容灾演练等场景中发挥作用。
- **分布式链路追踪 (Tracing)**：以网络为中心、面向服务请求的分布式追踪，清晰描绘系统调用层级与节点关系。提供大规模分布式系统中微服务交互的全景视图，保障复杂环境下的系统稳定性。
- **开源技术生态融合**：无缝对接 Prometheus、Grafana、Pyroscope、Elasticsearch 等主流开源可观测技术栈。支持物理机与云原生部署，自动感知 K8S 容器资源/标签/注解。通过零侵扰、内核可编程的 eBPF 技术，实现兼容主流硬件平台与 Linux 发行版。

## 整体大图

![](/docs/img/hardware-errors-huatuo-framework.png)

## 开源生态

![](/docs/img/huatuo-ecosystem.svg)

## 快速上手

- **极速体验**

  使用 Docker 一键启动华佗核心服务：
    ```bash
    $ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /run:/run huatuo/huatuo-bamai:latest
    ```

  在另一终端获取指标：
    ```bash
    $ curl -s localhost:19704/metrics
    ```

- **快速搭建**

  使用 Docker Compose 一键启动 Elasticsearch、Prometheus、Pyroscope、Grafana 和 huatuo 全栈服务：
    ```bash
    $ COMPOSE_PROFILES=full docker compose --project-directory ./build/docker up
    ```
  服务启动后，通过浏览器访问 http://localhost:3000 即可查看监控大盘。

  如需在不启动 `huatuo-apiserver` 的情况下展示 `cpuidle` 和 `cpusys` 火焰图，请参阅 [AutoTracing Pyroscope 火焰图](docs/development/autotracing_pyroscope_zh.md)。

    ![](/docs/img/quickstart-components.png)
  
    <div style="text-align: center; margin: 8px 0 20px 0; color: #777;">
    <small>
    HUATUO 组件运行示意图<br>
    </small>
    </div>

- **注意**
  请勿将 latest 标签的镜像部署至生产环境，此为开发测试分支。请使用正式发版的镜像或二进制文件。

## 内核版本

支持 4.18 及之后的所有内核版本。以下为主要测试过的内核与操作系统发行版。

|  HUATUO      |  内核版本 |  操作系统发行版     |
| :---  |    :----  |  :--- |
| 1.0      | 4.18.x      | CentOS 8.x                                    |
| 1.0      | 5.4.x       | OpenCloudOS V8/Ubuntu 20.04                   |
| 1.0      | 5.10.x      | OpenEuler 22.03/Anolis OS 8.10                |
| 1.0      | 5.15.x      | Ubuntu 22.04                                  |
| 1.0      | 6.6.x       | OpenEuler 24.03/Anolis OS 23.3/OpenCloudOS V9 |
| 1.0      | 6.8.x       | Ubuntu 24.04                                  |
| 1.0      | 6.14.x      | Fedora 42                                     |
| 2.3.0    | 7.0.x       | Ubuntu 26.04                                  |


## 文档

完整文档请访问: [https://docs.huatuo.tech/](https://docs.huatuo.tech/)

## 社区共建
- ❇️ 真诚欢迎每一位用户、开发者、公司以及组织，使用华佗监控、积极反馈 Bug、提交功能需求、分享最佳实践，共建专业、活跃的华佗开源社区。
- ❤️ 华佗贡献者
<a href="https://github.com/ccfos/huatuo/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=ccfos/huatuo" />
</a>

## 联系我们
- 微信群（备注姓名+单位）和公众号：

![](/docs/img/contact-weixin.png)


## 开源协议
该项目采用 Apache License 2.0 协议开源，BPF 代码采用 GPL 协议。

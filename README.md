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
  <a href="./README_CN.md"><strong>中文文档</strong></a> ·
  <a href="https://docs.huatuo.tech/en/latest/quick-start/"><strong>Quick Start</strong></a> ·
  <a href="https://docs.huatuo.tech/"><strong>Documentation</strong></a> ·
</p>

---

## What is HUATUO

**HUATUO** is a cloud-native operating system observability project open-sourced by **DIDI** and incubated under the **CCF**. It delivers kernel-level observability for general-purpose cloud-native computing, AI computing, and bare-metal infrastructure services.
By integrating Linux kernel dynamic tracing technologies like **kprobe**, **tracepoint**, **ftrace**, and **eBPF**, HUATUO provides kernel-wide insights: finer-grained metrics, automatic context capture from kernel runtime, and intelligent tracing.
Deployed at scale in Didi’s production environment, HUATUO plays a key role in troubleshooting system failures, enhancing the high availability and performance of cloud-native operating systems.

HUATUO is now listed in the [CNCF Landscape](https://landscape.cncf.io/?item=observability-and-analysis--observability--huatuo).

## Key Features

- **Kernel-Wide Insight**: Leverages BPF to maintain performance overhead below 1%, delivering full-stack, low-level observability insights into Linux kernel subsystems like MM, CPU scheduling, networking, and block I/O.
- **Instant Observability**: An event-driven runtime context capture mechanism that instruments kernel slow paths. It automatically triggers on events such as page faults, scheduling delays, generating detailed data for immediate analysis.
- **AutoTracing**: Employs automated snapshot retention to resolve performance jitters typical in cloud‑native and AI infrastructure environments, tackling issues such as CPU idle drops, CPU sys spikes, I/O surges, and Loadavg spikes.
- **Continuous Profiling**: A comprehensive and continuous performance profiling of the operating system and applications, covering CPU, Memory, I/O, and Locks. This drives business innovation and plays a key role in Chaos, HA and Stability Engineering.
- **Distributed Tracing**: Network-centric distributed tracing for service requests, which maps system call hierarchies and node relationships. It provides end-to-end visibility of microservice interactions in large-scale distributed systems.
- **Ecosystem Integration**: Integration with mainstream open-source observability stacks like Prometheus, Grafana, Pyroscope, and Elasticsearch. It automatically associates K8s container labels/annotations. Achieved through zero-instrumentation, kernel-level programming with eBPF, ensuring broad compatibility across hardware platforms and Linux distributions.

## Big Picture

![](/docs/img/hardware-errors-huatuo-framework.png)

## Ecosystem

![](/docs/img/huatuo-ecosystem.svg)

## Getting Started

- **Quick Run**

  To launch the HUATUO service with Docker:

        $ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /run:/run huatuo/huatuo-bamai:latest

  To pull metrics from another terminal:

        $ curl -s localhost:19704/metrics

- **Quick Setup**

  To launch the full stack (Elasticsearch, Prometheus, Grafana, and huatuo) using Docker Compose:

        $ docker compose --project-directory ./build/docker up

  Once running, access the monitoring dashboard at http://localhost:3000.

  ![](/docs/img/quickstart-components.png)  


- **NOTE**

  Do not deploy images with the latest tag to production environments, as this is a development and testing image. Use a formal release image or binary.


## Kernel Versions

The project supports kernel version 4.18 and later. The following kernel and OS distribution are primarily tested.

| HUATUO | Kernel Version | OS Distribution                               |
| :----- | :------------- | :-------------------------------------------- |
| 1.0.0  | 4.18.x         | CentOS 8.x                                    |
| 1.0.0  | 5.4.x          | OpenCloudOS V8/Ubuntu 20.04                   |
| 1.0.0  | 5.10.x         | OpenEuler 22.03/Anolis OS 8.10                |
| 1.0.0  | 5.15.x         | Ubuntu 22.04                                  |
| 1.0.0  | 6.6.x          | OpenEuler 24.03/Anolis OS 23.3/OpenCloudOS V9 |
| 1.0.0  | 6.8.x          | Ubuntu 24.04                                  |
| 1.0.0  | 6.14.x         | Fedora 42                                     |
| 2.3.0  | 7.0.x          | Ubuntu 26.04                                  |

## Documentation

For more information, visit [https://docs.huatuo.tech](https://docs.huatuo.tech/)

## Community Co-Building

- ❇️ We welcome all users, developers, companies, and organizations to use Huatuo, report bugs, submit feature requests, share best practices, and help us build a professional and vibrant open-source community.
- ❤️ HUATUO Contributors
<a href="https://github.com/ccfos/huatuo/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=ccfos/huatuo" />
</a>

## Contact Us
- WeChat Group and Official Account:

![](/docs/img/contact-weixin.png)

## License

This project is open source under the Apache License 2.0. The BPF code is licensed under the GPL license.

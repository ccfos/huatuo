English | [简体中文](./README_CN.md)

![](./docs/huatuo-logo-v3.png)

# What is HUATUO

**HUATUO** is a cloud-native operating system observability project open-sourced by **DIDI** and incubated under the **CCF**. It focuses on providing deep, kernel-level observability for complex cloud-native. By integrating linux kernel dynamic tracking such as **kprobe**, **tracepoint**, **ftrace** and **eBPF**, it has achieved multi-dimensional kernel observability, such as more refined metrics, kernel runtime exception contexts, and automatic tracking. HUATUO has been deployed at scale in Didi's production environment and plays a critical role in troubleshooting system failure, enhancing the high availability and performance of the cloud-native operating system. Through continuous evolution, HUATUO aims to advance eBPF in observability toward lower overhead and greater efficiency. For more information: [https://huatuo.tech](https://huatuo.tech/).

# Key Features

- **Low-Overhead Kernel Observability**: Leverages eBPF to maintain performance overhead below 1%, delivering in-depth, low-level, and comprehensive observability into linux core subsystem, such as memory, cpu scheduling, networking, and block I/O.
- **Event-Driven Context Capture**: This automatically acquires runtime context by triggering on critical events such as page faults, scheduling delays, and lock contention. Each event generates detailed observable data - including register states, stack traces, task info, and resource usage - for immediate analysis.
- **AutoTracing**: Leverages heuristic tracking algorithms and automated snapshots for system troubleshooting. This approach resolves performance degradation in complex cloud-native environments, addressing issues such as CPU idle, CPU sys, I/O, and Loadavg.
- **Continuous Performance Profiling**: A comprehensive and continuous performance profiling of the operating system and applications, covering CPU, Memory, I/O, and Locks. This feature can help applications continuously iterate and release, and plays a key role in stress testing and fault injection.
- **Distributed Tracing**: Network-centric distributed tracing for service requests, which can map system calls and node relationships. This feature supports cross-node tracing in large-scale distributed systems and provides a comprehensive view of microservice interactions.
- **Integration with Open Source Ecosystem**: Deeply integrated with other open-source observability stacks, it can automatically associate K8S container tags, annotations, and Linux kernel events, breaking down data silos. Programming the Kernel with eBPF, which is zero instrumentation and programmable.

# Software Architecture

![](./docs/img/huatuo-arch.png)

# Getting Started

- **Quick Run**

  Use the docker cli to launch the huatuo service:

        $ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /run:/run huatuo/huatuo-bamai:latest

  Pull the metric on another terminal:

        $ curl -s localhost:19704/metrics

- **Quick Setup**

  Launch the Elasticsearch, Prometheus, Grafana, and huatuo services using docker compose. Once the services are running, access [http://localhost:3000](http://localhost:3000/) via your web browser.

        $ docker compose --project-directory ./build/docker up

  ![](./docs/img/quickstart-components.png)  

  For more information, please refer to: [Quick Start](./docs/quick-start.md) or [https://huatuo.tech/quickstart/](https://huatuo.tech/quickstart/)

# Kernel Versions

Supports kernel versions from 4.18 and later.

| HUATUO | Kernel Version | OS Distribution                               |
| :----- | :------------- | :-------------------------------------------- |
| 1.0.0  | 4.18.x         | CentOS 8.x                                    |
| 1.0.0  | 5.4.x          | OpenCloudOS V8/Ubuntu 20.04                   |
| 1.0.0  | 5.10.x         | OpenEuler 22.03/Anolis OS 8.10                |
| 1.0.0  | 6.6.x          | OpenEuler 24.03/Anolis OS 23.3/OpenCloudOS V9 |
| 1.0.0  | 6.8.x          | Ubuntu 24.04                                  |
| 1.0.0  | 6.14.x         | Fedora 42                                     |

# Documentation

For more information, visit [https://huatuo.tech](https://huatuo.tech/).

# Contact Us

@[hao022](https://github.com/hao022)
@[nashuiliang](https://github.com/nashuiliang)
@[fanzu8](https://github.com/fanzuba)

# License

This project is open source under the Apache License 2.0. The BPF code is licensed under the GPL license.

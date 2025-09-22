English | [简体中文](./README_CN.md)

![](./docs/huatuo-logo-v3.png)

# What is HUATUO

**HUATUO** is a cloud-native operating system observability project open-sourced by **Didi** and incubated under the **CCF**. It focuses on delivering deep, kernel-level observability for complex cloud-native environments. Built on [eBPF](https://docs.kernel.org/userspace-api/ebpf/syscall.html) technology, it integrates kernel dynamic tracing techniques such as [kprobe](https://www.kernel.org/doc/html/latest/trace/kprobes.html), [tracepoint](https://www.kernel.org/doc/html/latest/trace/tracepoints.html), and [ftrace](https://www.kernel.org/doc/html/latest/trace/ftrace.html) to provide multi-dimensional kernel observability: **1.** Fine-grained metrics for kernel subsystems **2.** Event-driven capture of kernel runtime context during anomalies **3.** Automated tracing (AutoTracing) and profiling (AutoProfiling) for sudden system performance spikes. HUATUO has established a comprehensive deep observability architecture for the Linux kernel. It is already deployed at scale within Didi's production environment, where it plays a critical role in various failure scenarios, effectively ensuring high availability and performance optimization for cloud-native operating systems. Through continuous technological evolution, HUATUO aims to advance eBPF technology in the cloud-native observability domain towards finer granularity, lower overhead, and higher efficiency. Visit our official website for more information: [https://huatuo.tech](https://huatuo.tech/).

# Core Features

- **Low-Overhead Comprehensive Kernel Observability**: Leverages BPF technology to maintain performance overhead below 1%, enabling fine-grained, full-dimensional observation of core modules including memory management, CPU scheduling, network, and block I/O subsystems.
- **Anomaly Event-Driven Diagnostics**: Implements a runtime context capture mechanism driven by anomalous events, with precise instrumentation for kernel exceptions and slow paths. Automatically triggers tracing during critical events like page faults, scheduling delays, and lock contention, generating diagnostic information that includes register states, stack traces, and resource usage.
- **Fully Automated Tracing (AutoTracing)**: Utilizes heuristic tracing algorithms to address typical performance spike issues in complex cloud-native environments. Provides automated snapshot retention and root cause diagnosis for challenging problems such as CPU idle drops, CPU sys surges, I/O spikes, and Loadavg surges.
- **Continuous Performance Profiling**: Conducts ongoing, comprehensive performance profiling of the operating system kernel and applications, covering CPU, memory, I/O, locks, and various interpreted programming languages to support continuous business optimization and iteration. This feature is particularly useful in scenarios like stress testing, fire drills, and peak traffic management.
- **Distributed Tracing**: Offers network-centric, service request-oriented distributed tracing. Clearly delineates system call hierarchies, node relationships, and latency accounting. Supports cross-node tracing in large-scale distributed systems, providing a comprehensive view of microservice calls to ensure system stability in complex environments.
- **Integration with Open Source Ecosystem**: Seamlessly integrates with mainstream open-source observability stacks like Prometheus, Grafana, Pyroscope, and Elasticsearch. Supports deployment on standalone physical machines and in cloud-native environments. Automatically detects Kubernetes container resources, labels, and annotations, and correlates operating system kernel event metrics to eliminate data silos. Ensures broad compatibility with mainstream hardware platforms and kernel versions through a non-intrusive, kernel-programmable approach.

# Software Architecture

![](./docs/img/huatuo-arch.png)

# Getting Started

- **Quick Run**
  If you are primarily interested in the underlying principles and not concerned with storage or frontend display, we provide a pre-built image that includes the necessary components for running HUATUO. Simply execute:

    ```bash
  $ docker run --privileged --cgroupns=host --network=host -v /sys:/sys -v /run:/run huatuo/huatuo-bamai:latest
    ```

  In a separate terminal, retrieve metrics with:

    ```bash
  $ curl -s localhost:19704/metrics
    ```

- **Quick Setup**
  To gain a deeper understanding of HUATUO's operational mechanisms and architectural design, you can easily set up all components required for a full deployment locally. We provide container images and straightforward configurations to help users and developers quickly get acquainted with HUATUO.
    ![](./docs/img/quickstart-components.png)

    <div style="text-align: center; margin: 8px 0 20px 0; color: #777;">
    <small>
    HUATUO Component Operation Diagram<br>
    </small>
    </div>


  For a rapid environment setup, we offer a one-command startup method. This command will launch [elasticsearch](https://www.elastic.co), [prometheus](https://prometheus.io), [grafana](https://grafana.com), and the huatuo-bamai component. Once the command executes successfully, open your browser and go to [http://localhost:3000](http://localhost:3000) to view the monitoring dashboards.

    ```bash
  $ docker compose --project-directory ./build/docker up
    ```

  For more detailed information, please refer to: [Quick Start](./docs/quick-start.md) or [https://huatuo.tech/quickstart/](https://huatuo.tech/quickstart/)

# Kernel Versions

Supports all kernel versions after 4.18. Primary tested kernels and operating system distributions include:

| HUATUO | Kernel Version | OS Distribution                               |
| :----- | :------------- | :-------------------------------------------- |
| 1.0    | 4.18.x         | CentOS 8.x                                    |
| 1.0    | 5.4.x          | OpenCloudOS V8/Ubuntu 20.04                   |
| 1.0    | 5.10.x         | OpenEuler 22.03/Anolis OS 8.10                |
| 1.0    | 6.6.x          | OpenEuler 24.03/Anolis OS 23.3/OpenCloudOS V9 |
| 1.0    | 6.8.x          | Ubuntu 24.04                                  |
| 1.0    | 6.14.x         | Fedora 42                                     |

# Documentation

For more information, visit our official website: [https://huatuo.tech](https://huatuo.tech/)

# Contact Us

@[hao022](https://github.com/hao022)
@[nashuiliang](https://github.com/nashuiliang)
@[fanzu8](https://github.com/fanzuba)

# License

This project is open source under the Apache License 2.0. The BPF code is licensed under the GPL license.

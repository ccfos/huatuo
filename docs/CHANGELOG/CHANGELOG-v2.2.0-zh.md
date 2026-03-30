---
title: 变更日志
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 50
---

#### 特性

- 增加 iotracing autotracing 功能
- 增加通用硬件（cpu, memory, pcie）故障检测功能
- 增加 MetaX GPU 故障检测功能
- 增加支持物理链路检测功能
- 增加支持 Amazon EKS 部署
- 增加支持 Aliyun ACK 部署
- 增加 dropwatch namespace cookie 功能
- 增加容器 throttled_time 指标
- 增加兼容 kubelet systemd cgroupdriver 功能
- 增加自动化检测 kubelet cgroupdriver 类型
- 增加、优化、标准化 huatuo-bamai 配置文件
- 增加 Github CI/CD 自动化测试
- 增加单元测试，集成测试，端到端测试
- 增加丰富 golangci-lint 静态代码检查
- 增加 daemonset yaml 部署文件
- 增加 metric 新API接口
- 增加 5.15.x 内核兼容性适配

#### BUG 修复/优化

- 优化本地存储格式
- 若干模块代码优化，重构
- 优化丰富文档 https://huatuo.tech/docs/

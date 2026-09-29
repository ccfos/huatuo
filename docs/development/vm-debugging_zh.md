---
title: 在 GitHub Actions 中调试虚拟机
type: docs
description:
author: HUATUO Team
date: 2026-09-29
weight: 6
---

使用 `OS Distro QEMU Test` workflow 可以在支持的 Linux 虚拟机中测试分支，
无需在本地准备虚拟机。

## 准备 Fork 仓库

1. Fork 仓库，并在 fork 仓库的 **Actions** 页面启用 workflows。
2. 将 SSH 公钥添加到
   [GitHub 账号](https://github.com/settings/keys)。只有启动 workflow 的用户
   可以连接。
3. 将需要调试的分支 push 到 fork 仓库。

## 启动虚拟机

1. 在 fork 仓库中打开 **Actions > OS Distro QEMU Test > Run workflow**。
2. 选择需要调试的分支。
3. 在 **os** 中填写一个发行版，例如 `ubuntu24.04`。留空会为所有支持的
   发行版分别启动虚拟机。
4. 在 **Debug VM** 选项中选择保留虚拟机的时机和时长。
5. 启动 workflow，并打开对应的 `Test in VM` job。

如果没有 **Run workflow** 按钮，请先同步 fork 仓库的默认分支。GitHub 只会
显示默认分支中存在的手动 workflow。

选择 **Before test** 可以在测试前检查或修改虚拟机；选择 **After test on
failure** 可以排查失败测试；选择 **After test always** 会在测试结束后保留
虚拟机。

## 连接虚拟机

打开 job 中的 `Debug VM: Retain ...` step，并按日志提示操作：

1. 在本地终端运行日志中的完整 SSH 命令，连接 GitHub runner。
2. 在 runner 中运行日志中的 `ssh -i ... root@<VM_IP>` 命令，进入虚拟机。

日志中的命令已经包含正确的密钥路径和虚拟机地址，直接复制即可。

## 结束调试

退出虚拟机，然后在 runner 中释放 workflow：

```bash
cd "$GITHUB_WORKSPACE"
touch continue
```

workflow 随后继续执行测试或清理。仅断开 SSH 不会释放 workflow；如果不执行
上述命令，workflow 会一直等待到所选保留时间结束。

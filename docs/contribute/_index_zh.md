---
title: 贡献
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 40
---

# 如何贡献

HUATUO 在 [GitHub](https://github.com/ccfos/huatuo) 上进行开发，欢迎各种形式的贡献。建议先阅读架构文档，了解项目的高层目标。

## 克隆与环境准备

1. 确保你拥有 GitHub 账号。

2. Fork [huatuo](https://github.com/ccfos/huatuo) 仓库到你的 GitHub 用户或组织。

3. 按照 [GitHub 文档](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository#managing-github-actions-permissions-for-your-repository) 关闭 fork 仓库的 GitHub Actions，以避免 fork 上产生无意义的 CI 通知。

4. 克隆你的 fork 并添加上游仓库：

    ```
    git clone https://github.com/${YOUR_GITHUB_USERNAME_OR_ORG}/huatuo.git
    cd huatuo
    git remote add upstream https://github.com/ccfos/huatuo.git
    ```

5. 完成开发环境配置。

6. 查看 [GitHub Issues](https://github.com/ccfos/huatuo/issues) 寻找适合入门的任务。

## 开发者原创声明（DCO）

HUATUO 要求所有贡献附带 Developer Certificate of Origin（DCO）。只需在 commit message 末尾添加 `Signed-off-by` 行，标准格式如下：

```
fix: resolve authentication bug

The login token was not being validated properly.

Closes #123

Signed-off-by: Your Name <name@example.org>
```

签署即表示你已阅读并理解 DCO 内容。

## 运行测试

许多测试需要特权来设置资源限制并加载 eBPF 代码，建议使用 sudo 运行。运行全部测试：

```
make all
go test ./...
```

对当前包运行 Go linter：

```
make check
```

## 提交 Pull Request

1. 先创建 Draft Pull Request。在 GitHub「New Pull Request」页面，点击创建按钮旁的下拉箭头，选择「Create draft pull request」。工作尚未完成时建议使用此模式，CI 仍会运行。

    ![](/docs/img/how-to-contribute-pull-request.png)

2. 自测完成后，点击页面底部的 **Ready for review** 通知审阅者。

3. 根据审阅意见修改代码。修改期间可将 PR 设回 Draft，完成后再次点击 **Ready for review** 请求审阅。

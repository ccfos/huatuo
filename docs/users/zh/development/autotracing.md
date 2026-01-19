---
title: 自定义追踪
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 4
---

`AutoTracing` 与 `Event` 类型在框架实现上没有区别，只是针对不同的场景进行应用区分。

```go
type ITracingEvent interface {
    Start(ctx context.Context) error
}
```

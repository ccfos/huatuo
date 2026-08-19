---
title: Change Log
type: docs
description:
author: HUATUO Team
date: 2026-03-29
weight: 50
---

## Unreleased

### Changed

- Daemon shutdown now waits for tracing and active toolstream handlers before
  closing dependent BPF and storage resources. Terminal shutdown errors are
  returned while cleanup of independent resources continues.

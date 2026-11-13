---
title: Developer Guide
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 8
---

Read the [contribution guide](/docs/contribute/contribute_code_en.md) before making changes, and prepare the environment using [Building from Source](/docs/compiling_en.md).

| Development Task | Document |
| --- | --- |
| Add metrics, events, or automatic tracing | [Extending Features](/docs/development/custom-observability_en.md) |
| Define BPF events and generate Go types | [BPF ABI Guide](/docs/development/bpf_abi_codegen_en.md) |
| Standardize field semantics and timestamps | [Field Conventions](/docs/development/event-field-naming_en.md) |
| Debug components, inspect BPF logs, or troubleshoot in a VM | [Debugging](/docs/development/debugging_en.md) |
| Verify behavior across components | [Integration Tests](/docs/development/integration_en.md) |
| Understand TCP retransmission and drop correlation design | [Retransmission and Drop Correlation Design](/docs/development/tcp_retransmit_dropwatch_correlation_en.md) |
| Understand storage write and query contracts | [Storage Interfaces](/docs/development/storage_en.md) |

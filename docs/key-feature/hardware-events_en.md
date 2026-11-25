---
title: Hardware Error Diagnostics
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

## Overview

HUATUO records CPU, memory, and PCIe errors through kernel RAS events, providing error counts and structured details. Analyze failures using device locations, error trends, and application anomalies. A single error or an increase in counts does not by itself prove that a device is about to fail.

## Collection scope

| Source | `tracer_data.event` | Main information |
| --- | --- | --- |
| MCE | `MCE` | CPU machine-check registers, CPU IDs, and bank numbers |
| EDAC | `EDAC` | Memory errors, controllers, and memory location labels |
| ACPI GHES | `NON_STANDARD` | Platform-specific non-standard error records and FRU information |
| PCIe AER | `AER` | PCIe device, error type, and TLP header |
| MCE threshold interrupt | `MCE_THRESHOLD` | Interrupt vector and CPU ID |

Actual coverage depends on hardware, firmware, kernel tracepoints, and probe loading. Current startup checks require the `ras` tracepoint directory and `mce/mce_record`; the RAS observer does not start if either is missing. Although the decoder defines ARM GHES records, this does not establish ARM platform availability.

Kernel events pass through eBPF buffers to user space, where they are parsed into count metrics and event records:

![Hardware error collection and storage flow](/docs/img/hardware-errors-huatuo-framework.png)

## Configuration

Ensure that the global blacklist does not disable `ras`, and enable Elasticsearch or local file storage as needed. See “Hardware errors (RAS)” in [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md).

`EventTracing.Ras.MceThrBackoff` controls the interval between saved MCE threshold interrupt events. The built-in default is 1800 seconds. Received THR events are still counted during cooldown, so count increases may differ from the number of saved records.

## Results

### Error counts

`huatuo_bamai_ras_hw_err_total` counts received RAS events by source to show error trends. It is neither the sum of `err_count` values in EDAC records nor the number of saved records.

### Event records

Events include observation time, device, error source, severity, and source-specific details. The following example omits some fields:

```json
{
    "tracer_name": "ras",
    "tracer_type": "event",
    "tracer_data": {
        "dev": "MEM",
        "event": "EDAC",
        "type": "Corrected",
        "info": "{\"err_count\":1,\"label\":\"CPU0_DIMM_A1\"}"
    }
}
```

Query by `tracer_name=ras`, device, and time range. `info` is a JSON string that requires a second parsing step; its fields vary by error source. Assess impact using device topology and error trends. A single record does not by itself prove device damage.

### Severity

| Type | Meaning |
| --- | --- |
| `Corrected` | Corrected |
| `UncorrectedRecoverable` | Uncorrected, recoverable category |
| `UncorrectedDeferred` | Uncorrected, deferred category |
| `UncorrectedFatal` | Uncorrected, fatal category |
| `Info` | Informational record |
| `unknown` | Severity cannot be mapped |

Severity is a classification. It does not establish that recovery succeeded, that the application was unaffected, or that a restart is required. For registers, raw code mappings, and fields by source, see [Hardware Event Fields](/docs/reference/hardware-events_en.md).

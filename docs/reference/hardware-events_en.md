---
title: Hardware Event Fields
type: docs
description:
author: HUATUO Team
date: 2026-03-06
weight: 4
---

This reference covers RAS metrics, event structures, severity, and source fields. See [Hardware Fault Diagnosis](/docs/key-feature/hardware-events_en.md) for coverage and enablement.

`EventTracing.Ras.MceThrBackoff` controls the save interval for threshold-interrupt events. Events are still counted during cooldown, so counter increases need not equal the number of saved records.

## Results

### Error Counts

`huatuo_bamai_ras_hw_err_total` counts received RAS events by `type`: `mce`, `edac`, `acpi`, `aer`, `thr`, and `arm_ghes`. The presence of a label does not prove that the current platform supports its probe.

```prometheus
huatuo_bamai_ras_hw_err_total{hostname="node-01",region="region-01",type="edac"} 12
```

These are example values. The unit is events, not the sum of EDAC `err_count` values. See [Kernel-Wide Insight](/docs/key-feature/kernel-wide-insight_en.md) for NIC hardware drops and PFC metrics, and [Abnormal Event Diagnosis](/docs/key-feature/instant-observability_en.md) for NIC state changes.

### Event Records

Query with `tracer_name=ras` and `tracer_type=event`. This EDAC example omits some fields:

```json
{
    "hostname": "node-01",
    "observed_timestamp": "2026-05-11T08:58:47.329Z",
    "kernel_observed_timestamp": "2026-05-11T08:58:47.328548319Z",
    "tracer_name": "ras",
    "tracer_type": "event",
    "tracer_data": {
        "dev": "MEM",
        "event": "EDAC",
        "type": "Corrected",
        "info": "{\"err_count\":1,\"err_type\":\"Corrected\",\"err_msg\":\"memory read error\",\"label\":\"CPU_SrcID#0_Ha#0_Chan#0_DIMM#0\",\"mc_index\":0,\"addr\":7860269056,\"grain\":128}"
    }
}
```

| Field | Description |
| --- | --- |
| `tracer_data.dev` | Device or component identifier, such as `CPU/MEM`, `MEM`, `ACPI`, or `PCIe 0000:03:00.0` |
| `tracer_data.event` | Error source; see [Coverage](/docs/key-feature/hardware-events_en.md#collection-scope) |
| `tracer_data.type` | Normalized severity; see below |
| `tracer_data.info` | JSON **string** containing details; parse it again. Its structure depends on the event source |
| `observed_timestamp` | Time when user space receives the event |
| `kernel_observed_timestamp` | Timestamp converted from kernel observation time |

### Severity

Severity classifies the event. It does not establish successful recovery, absence of application impact, or a need to reboot.

| `type` | Meaning | Current Mapping |
| --- | --- | --- |
| `Corrected` | Corrected | MCE without UC/DEFERRED flags, EDAC 0, ACPI 1, AER 2, THR |
| `UncorrectedRecoverable` | Uncorrected, recoverable category | MCE UC, EDAC 1, ACPI 2, AER 0 |
| `UncorrectedDeferred` | Uncorrected, deferred category | MCE DEFERRED, EDAC 2 |
| `UncorrectedFatal` | Uncorrected, fatal category | EDAC 3, ACPI 3, AER 1 |
| `Info` | Informational record | EDAC 4, ACPI 0 |
| `unknown` | Unmapped severity | Unrecognized raw values or sources without severity |

If MCE sets both DEFERRED and UC, `UncorrectedDeferred` takes precedence.

## Event Details

The following fields are in the parsed `tracer_data.info`.

### MCE

Register and bank meanings depend on the CPU model. Use the corresponding processor manual for diagnosis.

| Field | Description |
| --- | --- |
| `mcg_cpu_cap` / `mcg_msr_status` | Global capability register `MCG_CAP` and status register `MCG_STATUS` |
| `banks_msr_status` | Bank status register `MCi_STATUS`, including error codes and UC, DEFERRED, ADDRV, and MISCV flags |
| `banks_msr_addr` | `MCi_ADDR` error address; interpret only when ADDRV is valid |
| `banks_msr_misc` | `MCi_MISC` details; interpret only when MISCV is valid |
| `mca_synd_msr` / `mca_ipid_msr` | AMD syndrome and instance ID registers |
| `instr_pointer` / `code_seg` | Instruction pointer and code segment; check `MCG_STATUS` for instruction-pointer validity |
| `tsc_timestamp` | CPU timestamp counter, not directly usable as a Unix timestamp |
| `walltime` | Unix timestamp in seconds |
| `cpu` / `cpuid` / `apicid` / `socketid` | Logical CPU, CPUID, APIC ID, and CPU socket ID |
| `bank` | Machine Check Bank ID; component mapping is platform-dependent |
| `cpuvendor` | CPU vendor code recorded by the kernel |

### MCE Threshold Interrupts

| Field | Description |
| --- | --- |
| `vector` | Interrupt vector |
| `cpu` | Logical CPU ID |

### EDAC

| Field | Description |
| --- | --- |
| `err_count` | Error count reported by this event |
| `err_type` / `err_msg` | Normalized severity and error description |
| `label` | Driver-reported memory location label; confirm the physical slot using machine topology |
| `mc_index` | Memory controller index, starting at 0 |
| `top_layer` / `mid_layer` / `low_layer` | Driver-defined memory topology indexes; `-1` means unspecified |
| `addr` | Error address reported by the kernel |
| `grain` | Address granularity in bytes |
| `syndrome` | ECC syndrome value |
| `driver` | Additional error information from the driver, not necessarily its name |

### ACPI GHES

| Field | Description |
| --- | --- |
| `severity` | Raw severity value; see the severity table for current mappings |
| `sec_type` | Error section GUID as a 16-byte hexadecimal string |
| `fru_id` / `fru_text` | Replaceable component identifier and description, if supplied by firmware |
| `data_len` | Reported data length in bytes |
| `raw_data` | Hex dump; interpret using the platform and vendor format |

### PCIe AER

| Field | Description |
| --- | --- |
| `dev_name` | PCIe BDF address, such as `0000:03:00.0` (Domain:Bus:Device.Function) |
| `err_type` | `Corrected`, `UncorrectedRecoverable`, `UncorrectedFatal`, or `unknown` |
| `err_reason` | Error name for the AER status value; the parser matches a single status value and uses `unknown` if unmatched |
| `tlp_header` | Four hexadecimal words of the TLP header; `not available` if absent |

Error names describe the reported condition and do not prove device damage. The current parser recognizes the following status values.

#### Correctable Errors

| Status Value | `err_reason` |
| --- | --- |
| `0x00000001` | Receiver Error |
| `0x00000040` | Bad TLP |
| `0x00000080` | Bad DLLP |
| `0x00000100` | REPLAY_NUM Rollover |
| `0x00001000` | Replay Timer Timeout |
| `0x00002000` | Advisory Non-Fatal Error |
| `0x00004000` | Corrected Internal Error |
| `0x00008000` | Header Log Overflow |

#### Uncorrectable Errors

| Status Value | `err_reason` |
| --- | --- |
| `0x00000001` | Undefined |
| `0x00000010` | Data Link Protocol Error |
| `0x00000020` | Surprise Down Error |
| `0x00001000` | Poisoned TLP |
| `0x00002000` | Flow Control Protocol Error |
| `0x00004000` | Completion Timeout |
| `0x00008000` | Completer Abort |
| `0x00010000` | Unexpected Completion |
| `0x00020000` | Receiver Overflow |
| `0x00040000` | Malformed TLP |
| `0x00080000` | ECRC Error |
| `0x00100000` | Unsupported Request Error |
| `0x00200000` | ACS Violation |
| `0x00400000` | Uncorrectable Internal Error |
| `0x00800000` | MC Blocked TLP |
| `0x01000000` | AtomicOp Egress Blocked |
| `0x02000000` | TLP Prefix Blocked Error |

### ARM GHES

`ARM_GHES` records use severity `unknown`. See [Coverage](/docs/key-feature/hardware-events_en.md#collection-scope) for platform limitations.

| Field | Description |
| --- | --- |
| `mpidr` / `midr` | Hexadecimal processor identifiers; `mpidr=0x0` may also mean the kernel did not supply a value |
| `running_state` | `running`, `stopped`, or `N/A` |
| `psci_state` | Hexadecimal PSCI state when stopped; `N/A` when inapplicable |
| `affinity_level` | Decimal affinity level; `N/A` when unavailable |

---
title: Kata Guest collector
type: docs
weight: 30
---

Run huatuo-bamai inside a Kata VM as a privileged sidecar with access to the
complete Guest process and cgroup trees. Sharing the Pod process namespace is
necessary when processes are otherwise isolated, but it does not make sibling
cgroup directories visible through a private cgroup mount. Configure the Kata
deployment to expose the complete Guest trees and verify that they belong to the
Guest kernel. Mounting the physical Host process or cgroup tree into this sidecar
would observe the wrong kernel and is not supported by sandbox mode.

## Configuration

`Mode = "none"` (or an omitted Mode) preserves existing startup behavior.
`Mode = "baremetal"` selects Host operation. `Mode = "sandbox"` replaces kubelet
and Host runtime discovery with Guest-local process and cgroup discovery.
Changing Mode through the configuration API requires a restart.

Use `huatuo-bamai-sandbox.conf` and replace its API bearer token. Supply these
environment variables through Kubernetes Downward API or equivalent configuration:

| Variable | Value |
| --- | --- |
| BAMAI_POD_NAME | metadata.name |
| BAMAI_POD_NAMESPACE | metadata.namespace |
| BAMAI_NODE_NAME | spec.nodeName |

Start the sidecar with:

```sh
huatuo-bamai --region YOUR_REGION --config-dir /path/to/conf \
  --config huatuo-bamai-sandbox.conf --bpf-dir /path/to/bpf \
  --tools-bin-dir /path/to/bin
```

The Guest must permit BPF loading and tracefs/debugfs mounts. Existing mounts are
reused; missing mounts are prepared before BPF initialization, including the
legacy debugfs tracing layout. Kernels without tracing support log a warning;
local container discovery does not depend on BPF probe availability. CO-RE
collectors also require Guest kernel BTF; successful agent startup does not verify
those collectors. Authentication
requirements are unchanged. Protect the Guest API token and network exposure as
for any other Node agent. The token in the sample is a placeholder.

Self cgroup limits remain disabled by default. If `--enable-cgroup` is requested
but the Guest lacks systemd resource management, sandbox mode logs the failure
and continues without that limit. Errors remain fatal in Host modes.

## Container discovery

Discovery runs every five seconds and excludes the collector's own container.
It groups visible processes by container ID, prefers a process whose parent is
outside the container, and tracks process start time and memory directory identity.
PID reuse creates a new lifecycle generation. Partial scans retain previously
known containers; successful empty scans remove exited containers.

Both unified cgroup v2 and separate cgroup v1 CPU/memory trees are supported.
Namespace-relative `/../CONTAINER_ID` paths are resolved only when the directory
match is unique. Valid absolute paths are preserved. Guest discovery feeds the
existing container API and lifecycle subscribers without calling kubelet or
reading Host runtime PID files. Pod labels come from the configured Guest identity;
full Host metadata enrichment belongs to the Host ingestion path.

## Current data path

This configuration stores Guest observations locally using the current document
format and the physical node name from BAMAI_NODE_NAME. It does not enable
Guest-to-Host VSOCK forwarding. That transport and Host-side peer authentication
are separate functionality; keep the collector API's existing authentication
when inspecting local results.

VSOCK forwarding is available as an explicit opt-in; see [Guest-to-Host transport](kata_transport_en.md) for authenticated Host listeners, queue limits and delivery semantics.

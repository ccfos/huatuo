# Kata Guest-to-Host document transport

Enable the Host receiver in the node agent configuration:

```toml
[GuestBridge]
Enabled = true
Port = 19705
MaxConnections = 512
MaxMessageBytes = 16777216
MaxInFlightBytes = 268435456
HandshakeTimeoutSeconds = 10
FirecrackerRoot = "/run/vc/firecracker"
```

Host Pod discovery must remain enabled. The agent needs the Host VSOCK device,
Host `/proc` runtime process view and Firecracker runtime directory. QEMU uses
Host CID 2. Firecracker uses its existing hybrid VSOCK channel and a per-sandbox
`root/kata.hvsock_19705` listener. Unix credentials must identify the actual
Host hypervisor PID. Keep the parent runtime directory protected from workloads.
The Host must observe the same PID namespace as these runtime processes.

Enable the sender in each Kata Guest agent configuration:

```toml
Mode = "sandbox"

[GuestTransport]
Enabled = true
Port = 19705
MaxMessageBytes = 16777216
MaxPendingBytes = 67108864
IOTimeoutSeconds = 10
```

Supply `BAMAI_POD_NAME`, `BAMAI_POD_NAMESPACE` and `BAMAI_NODE_NAME` as described
in the Guest deployment guide. Guest transport replaces local/Elasticsearch
persistence for tracing and standalone profiling windows. `--disable-storage`
still takes precedence. Each stream has its own byte-bounded queue.

The Host saves tracing events, automatic tracing results and JSON flame/snapshot
results through its normal tracing Store, including file storage and event
subscriptions. Typed profiling windows use its profiling Store; configure the
Host Elasticsearch backend to persist them. Without that backend profiles are
rejected. Tracing subscriptions and CloudEvents retain their existing filters.

Both switches default to false. Mode, transport and bridge changes require a
restart; runtime configuration updates reject these changes. Sender and receiver
ports must agree. A sender frame must fit the receiver's MaxMessageBytes; the
receiver's total budget must fit at least one maximum frame.

Delivery is best effort. An initial/disconnected, full or oversized send returns
an error. A broken connection drops its queue, then reconnects. Close drains
accepted writes until the shutdown deadline. A successful send is not a Host
acknowledgement or proof of durable storage. Guest APIs requiring synchronous
query visibility/publication are unavailable over this write-only backend.
Standalone profiling can be forwarded; profiling publication markers are not.

Host attribution comes from runtime metadata and the authenticated connection.
Guest-supplied Host identity is replaced; foreign container ownership is rejected.
VM-wide documents retain the authenticated namespace and clear container metadata.
Producer observation times remain intact; the Host assigns upload time.

For real channel tests, build the helper in
`internal/guestbridge/testdata/sender` and the integration-tagged guestbridge test.
`TestKataVSOCKIntegration` requires explicit selected-container metadata, a
nonprivileged port and a ready path through `HUATUO_KATA_BRIDGE_*` environment
variables. Use disposable Kata Pods and remove them and their sockets afterward.
The fixture validates native profiling encoding against an HTTP test server;
it does not replace validation against a real Elasticsearch deployment.

FirecrackerRoot must match the runtime directory (for example, newer Kata
installations may use `/run/kata/firecracker`).

Guest profiling remains best-effort: a successful write only means the window
entered the local queue. Disconnects, shutdown, or Host storage failures may
lose it. The Host uses synchronous storage for received profiling windows, but
no durable completion acknowledgement is returned to the Guest. Visibility
requests and completion-marker operations return an unsupported-operation
error. A profiling task that requires a commit acknowledgement therefore
reports failure even if some windows have reached Host storage. Do not treat
that task as durably completed. Commit frames sent to GuestBridge are rejected.

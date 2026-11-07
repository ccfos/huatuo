# Authenticated Kata GuestBridge

The bridge is disabled until explicitly started. QEMU VSOCK connections are
bound to a Host-discovered sandbox by CID. The runtime hypervisor PID must
have a QEMU command line naming the same sandbox and the matching VSOCK CID.
Ambiguous bindings fail closed. Missing discovery can wait up to the configured
handshake timeout, and shutdown cancels that wait.

Firecracker uses sandbox-specific `root/kata.hvsock_<port>` Unix listeners.
The kernel-provided Unix peer PID must equal that sandbox's Host-discovered
hypervisor PID. The socket path alone does not authenticate a peer. Active
listeners and non-socket files are never removed as stale sockets. A closing
listener only unlinks the socket it bound; a path replaced by another process
is left untouched.

Each document carrying a container ID must match the Host's exact container ID,
Kata sandbox and namespace. Host region, hostname, source and container metadata
replace Guest-supplied values, including on VM-wide documents. Tracer IDs are
hashed with the authenticated sandbox ID and prefixed with `kata-`.

`kata-tracing-document` is decoded with JSON number preservation and enters the
existing tracing Store, including local files and subscriptions. Typed profiles
use `kata-profiling-document` and the profiling Store; an unavailable profiling
backend rejects the result. Producer timestamps are preserved. Receiving stores
assign their upload timestamps. Unsupported streams and unsafe names are rejected.

All listeners share a connection cap and encoded-frame decode budget. Defaults
are 512 connections, 16 MiB per frame, 256 MiB in flight, ten seconds for
handshake/discovery and ten seconds for an active frame. Idle connections hold
no frame budget. These bounds are not a cap on total process memory.

Shutdown cancels all listeners and joins handlers before closing dependent
stores. If the shutdown context expires, those stores must remain open until
a repeated shutdown joins all handlers. Prometheus metrics report active
connections, listener managers, accepted/rejected documents and frame errors.
The Firecracker manager gauge indicates scanning, not a connected Guest.

Successful writes have no application acknowledgement. This path does not
provide durable delivery or remote query visibility guarantees.

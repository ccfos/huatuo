# Kata document sender

The write-only `internal/storage/vsock` backend sends complete JSON documents
using Toolstream protocol version 1. Tracing and typed profiling documents use
separate connections (`kata-tracing-document` and `kata-profiling-document`).
The Host chooses the destination collection; the sender cannot select it.

`Options` defaults to Host CID 2, port 19705, 16 MiB per encoded frame,
64 MiB per sender queue, ten seconds per dial/write and one second between
reconnect attempts. The queue includes the current write. Idle connections
hold no frame reservation. The dial uses a cancellable socket connection.

Save copies accepted data and returns without waiting for delivery. Disconnected,
oversized and full-queue writes return errors. A broken connection drops its
remaining queue. Close rejects writes, drains until its context expires, cancels
network work, and joins the sender. There is no acknowledgement, replay or
remote visibility guarantee; even a successful send can be lost at the Host.
Conditional writes and synchronous visibility requests are unsupported.

The backend does not start a Host listener. An authenticated receiver and
explicit daemon configuration are required before enabling the complete path.

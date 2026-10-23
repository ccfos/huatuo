# Toolstream transport boundaries

The Unix socket client and `Serve` use protocol version 3. Service names route
connections independently of tool identity. `NewClientWithConn` accepts ownership
of any `net.Conn` and a `ClientMetadata` value (service name, tool name, version
and task ID). The transport package supplies the protocol version.
`NewClientWithConnContext` additionally closes the connection on cancellation,
including during a blocked handshake or send. `Send` accepts `SendOptions`;
`Commit` requires `End` and waits for the server's acknowledgement. `Close`
releases the connection without sending a frame.

`EncodeChunkFrame` returns one complete encoded data frame, including framing,
without writing it. Queueing senders encode a payload once and pass the frame
to `SendFrame`. These data frames do not request a commit acknowledgement.

`ServeWithOptions` adds opt-in connection context, handshake timeout, maximum
message size, shared decoding byte budget and per-frame receive timeout.
Connection context is attached before the handshake and is server-owned;
handshake TaskID remains untrusted peer metadata. Cancellation closes the
connection and interrupts idle or incomplete reads.

A `DecodeBudget` may be shared across servers. It reserves encoded frame bytes
before reading/allocating the body and holds them until the synchronous handler
returns. It excludes small bounded segment tables and connection metadata, so
it is not a process memory limit. Payloads retained after handler return must
be copied. Error paths release reservations. Idle connections reserve no frame
bytes. A frame receive deadline starts at the first byte and covers the
remaining header, budget wait and body. It does not restart on each byte.

For budgeted decoding, the default frame receive timeout is ten seconds.
`FrameReadTimeout` can override it or enable timed frame reads without a shared
budget. Either option requires an explicit `MaxMessageSize` of at least eight
bytes; for this limited decoder the maximum includes the segment table.
Timeouts must be nonnegative. Handshake timeout includes the wait for the first
byte. Unlimited options preserve the existing Cap'n Proto decoder defaults.

`Shutdown(ctx)` cancels connections and waits for handlers, bounded by ctx.
If it times out, handlers may still be using their dependencies. A later
`Shutdown` or `Close` must confirm completion before those dependencies close.
A failed optional identity callback rejects the connection before reading a
handshake. No VSOCK listener or Guest authorization policy is enabled here.

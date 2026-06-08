@0xc0aefe4459d6cfa3;

using Go = import "/go.capnp";
$Go.package("transport");
$Go.import("huatuo-bamai/internal/toolstream/transport");

# First frame sent by a client; carries connection-level metadata.
struct ConnectRequest {
    toolName     @0 :Text;
    version      @1 :Text;
    taskID       @2 :Text;
    protoVersion @3 :UInt32 = 1;
}

# Chunk is every subsequent data / control frame.
# Mirrors transport-design.md DataChunk.
struct Chunk {
    data        @0 :Data;    # opaque payload, handler decides how to parse
    flush       @1 :Bool;    # reserved; server may save accumulated buffer on true
    end         @2 :Bool;    # true marks a normal end of stream
    error       @3 :Text;    # non-empty marks a fatal error from the tool
    containerID @4 :Text;    # tool-declared container ID for this chunk; daemon
                             # uses it for storage routing; empty = unassociated
}

# Message is the top-level envelope, one ConnectRequest followed by one or more Chunks.
struct Message {
    union {
        connect @0 :ConnectRequest;
        chunk   @1 :Chunk;
    }
}

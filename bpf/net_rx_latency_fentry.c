// fentry-capable variant of net_rx_latency.
//
// It is the same source, compiled with HUATUO_RXLAT_FENTRY defined, so the
// object carries both entry points for tcp_v4_rcv: the kprobe program and the
// fentry program. The loader removes the entry point the kernel cannot use
// before loading the collection, because one unsupported program would fail
// the whole load.
//
// net_rx_latency.c stays kprobe-only: existing users keep loading exactly the
// program they loaded before, and the event algorithm, the maps and the ABI
// are shared with this object.

#define HUATUO_RXLAT_FENTRY 1

#include "net_rx_latency.c"

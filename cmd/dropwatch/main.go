// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
	"unsafe"

	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/linkstatus"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pcapfilter"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/dropwatch.c -o $BPF_DIR/dropwatch.o

var (
	dropwatchToolName = "dropwatch"
	AppVersion        = ""
)

// Must match struct perf_event_t in bpf/dropwatch.c exactly.
type packetMeta struct {
	KtimeNS            uint64
	TgidPid            uint64
	NetCookie          uint64
	SkbAddr            uint64
	NetdevIfindex      uint32
	NetdevFlags        uint32
	NetdevQueueMapping uint32
	DropSource         uint32
	Type               uint32
	Pad                uint32
	NetdevName         [bpf.NetdevNameLen]byte
	Comm               [bpf.TaskCommLen]byte
}

type packetRaw struct {
	EthProto  uint16
	RawLen    uint16
	HasEthHdr uint16 // 1: raw[] starts with Ethernet header; 0: starts at L3
	Pad       uint16
	PktLen    uint32
	SkState   uint32
	Raw       [packet.RawCapacity]byte
}

type dropPacketEvent struct {
	Meta      packetMeta
	Raw       packetRaw
	StackSize uint64
	Stack     [symbol.KsymStackMaxDepth]uint64
}

// Compile-time layout guards: assert BPF wire struct sizes match the C definitions.
var (
	_ = [1]struct{}{}[88-unsafe.Sizeof(packetMeta{})]
	_ = [1]struct{}{}[136-unsafe.Sizeof(packetRaw{})]
	_ = [1]struct{}{}[232-unsafe.Offsetof(dropPacketEvent{}.Stack)]
)

// loadDropwatchBPF reads the BPF object at bpfPath, injects filterExpr into the
// pcap_stub_l2/l3 stubs, and loads it. Each instance uses a unique BPF name to
// allow multiple instances to coexist.
func loadDropwatchBPF(bpfPath, filterExpr string) (bpf.BPF, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read bpf object: %w", err)
	}
	bpfName := fmt.Sprintf("dropwatch_%d.o", time.Now().UnixNano())
	return pcapfilter.Load(bpfName, bpfBytes, filterExpr, nil)
}

func formatEvent(ev *dropPacketEvent) *types.DropWatchTracing {
	pkt := packet.PacketHdr{
		EthProto:  ev.Raw.EthProto,
		RawLen:    uint8(ev.Raw.RawLen),
		HasEthHdr: uint8(ev.Raw.HasEthHdr),
		SkState:   uint8(ev.Raw.SkState),
		Raw:       ev.Raw.Raw,
	}

	pktType, pktInfo := packet.ParsePacketHdr(&pkt)

	frames := symbol.KsymStackStrs(ev.Stack[:], symbol.KsymStackMaxDepth)
	stackStr := strings.Join(frames, "\n")

	return &types.DropWatchTracing{
		ObservedTimestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Comm:               bytesutil.ToStr(ev.Meta.Comm[:]),
		Pid:                ev.Meta.TgidPid >> 32,
		NetdevName:         bytesutil.ToStr(ev.Meta.NetdevName[:]),
		NetdevIfindex:      ev.Meta.NetdevIfindex,
		NetdevQueueMapping: ev.Meta.NetdevQueueMapping,
		NetdevLinkStatus:   linkstatus.FlagsRaw(ev.Meta.NetdevFlags),
		PacketSkbAddr:      fmt.Sprintf("0x%016x", ev.Meta.SkbAddr),
		PacketEthProto:     fmt.Sprintf("0x%04x", ev.Raw.EthProto),
		PacketLen:          ev.Raw.PktLen,
		PacketType:         pktType,
		PacketInfo:         pktInfo,
		Stack:              stackStr,
	}
}

func mainAction(c *cli.Context) error {
	duration := c.Int("duration")
	outputFmt := c.String("output")

	if err := bpf.NewManager(&bpf.Option{KeepaliveTimeout: duration}); err != nil {
		return fmt.Errorf("dropwatch: init bpf manager: %w", err)
	}
	defer bpf.Close()

	// Connect to the output storage if --output-storage is specified.
	var sockClient *toolstream.Client

	if path := c.String("output-storage"); path != "" {
		var err error
		sockClient, err = toolstream.NewClient(toolstream.ClientOptions{
			SockPath: path,
			ToolName: dropwatchToolName,
			Version:  AppVersion,
			TaskID:   c.String("task-id"),
		})
		if err != nil {
			return fmt.Errorf("dropwatch: --output-storage: %w", err)
		}

		defer sockClient.End()
	}

	bpfObj, err := loadDropwatchBPF(c.String("bpf-path"), c.String("filter"))
	if err != nil {
		return fmt.Errorf("dropwatch: load bpf: %w", err)
	}
	defer bpfObj.Close()

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if duration > 0 {
		var dcancel context.CancelFunc
		runCtx, dcancel = context.WithTimeout(runCtx, time.Duration(duration)*time.Second)
		defer dcancel()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGINT, unix.SIGTERM)
	defer signal.Stop(sig)

	go func() {
		select {
		case <-sig:
			cancel()
		case <-runCtx.Done():
		}
	}()

	reader, err := bpfObj.AttachAndEventPipe(runCtx, "perf_events", 8192)
	if err != nil {
		return fmt.Errorf("dropwatch: attach: %w", err)
	}
	defer reader.Close()

	bpfObj.WaitDetachByBreaker(runCtx, cancel)

	sink := newWriter(outputFmt, sockClient)

	var ev dropPacketEvent

	for {
		if runCtx.Err() != nil {
			return nil
		}

		if err := reader.ReadInto(&ev); err != nil {
			if runCtx.Err() != nil {
				return nil
			}

			log.Errorf("dropwatch: read: %v", err)

			continue
		}

		if err := sink.Write(formatEvent(&ev)); err != nil {
			log.Errorf("dropwatch: send event: %v", err)
			return nil
		}
	}
}

func main() {
	app := &cli.App{
		Name:    dropwatchToolName,
		Version: AppVersion,
		Usage:   "watch kernel packet drops",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "bpf-path",
				Usage:    "path to the dropwatch BPF object file",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "filter",
				Usage: `tcpdump expression, e.g. "tcp and port 80"`,
			},
			&cli.IntFlag{
				Name:  "duration",
				Usage: "run for N seconds then exit (0=forever)",
			},
			&cli.StringFlag{
				Name:  "output",
				Value: "text",
				Usage: "output format: json or text; ignored when --output-storage is set",
			},
			&cli.StringFlag{
				Name:  "output-storage",
				Usage: "unix socket path to send events to; when set, --output is ignored",
			},
			&cli.StringFlag{
				Name:  "task-id",
				Usage: "task ID to associate with this session (requires --output-storage)",
			},
		},
	}

	app.Action = mainAction
	app.Before = func(c *cli.Context) error {
		if v := c.String("output"); v != "json" && v != "text" {
			return fmt.Errorf("--output: invalid value %q, want json or text", v)
		}
		if c.IsSet("output") && c.String("output-storage") != "" {
			log.Warnf("--output is ignored because --output-storage is set")
		}
		return nil
	}

	if err := app.Run(os.Args); err != nil {
		log.Errorf("%v", err)
		os.Exit(1)
	}
}

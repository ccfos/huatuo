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

//go:build integration

// test_net_rx_tracing loads net_rx_latency through the real loaders with
// nanosecond thresholds and reports the latency stage of every event it
// receives. stdout carries a ready marker once the object is attached, one JSON
// line per event, and a summary line last.
//
// The thresholds are a fixture concern only: production configuration cannot
// express them, and they exist so a test can assert which stage arrives
// instead of waiting for a latency that may never exceed the production
// threshold in a virtual network.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/utils/netutil"

	"golang.org/x/sys/unix"
)

const (
	// modeAuto loads the object that carries both entry points and lets the
	// kernel decide.
	modeAuto = "auto"
	// modeFentry is modeAuto plus a demand that fentry was selected.
	modeFentry = "fentry"
	// modeKprobe loads the kprobe-only object, which is the path kernels
	// without fentry support take.
	modeKprobe = "kprobe"

	eventMap     = "net_recv_lat_event_map"
	kprobeObject = "net_rx_latency.o"
	fentryObject = "net_rx_latency_fentry.o"

	kprobeProgram = "tcp_v4_rcv_prog"
	fentryProgram = "tcp_v4_rcv_fentry_prog"

	// fixtureThresholdNS is low enough that every packet with a timestamp
	// passes the latency check.
	fixtureThresholdNS = 1
)

var stageNames = []string{
	"RX_STAGE_NETIF",
	"RX_STAGE_TCPV4",
	"RX_STAGE_USERCOPY",
}

type config struct {
	bpfDir      string
	mode        string
	timeout     time.Duration
	maxEvents   int
	thresholdNS int64
}

// eventRecord mirrors the tracer data the integration script asserts on.
type eventRecord struct {
	Event      string `json:"event"`
	Stage      string `json:"stage"`
	LatencyNS  uint64 `json:"latency_ns"`
	Saddr      string `json:"saddr"`
	Sport      uint16 `json:"sport"`
	Daddr      string `json:"daddr"`
	Dport      uint16 `json:"dport"`
	Seq        uint32 `json:"seq"`
	PacketLen  uint64 `json:"packet_len_bytes"`
	Duplicate  bool   `json:"duplicate"`
	EntryPoint string `json:"entry_point"`
}

// readyMarker is printed once the object is attached: a caller that measures
// the hook must start its traffic after the attach, not while the loader is
// still probing the kernel.
type readyMarker struct {
	Ready      bool   `json:"ready"`
	Mode       string `json:"mode"`
	EntryPoint string `json:"entry_point"`
}

// summary is the last line the fixture prints; the script reads it.
type summary struct {
	Summary     bool     `json:"summary"`
	EntryPoint  string   `json:"entry_point"`
	Mode        string   `json:"mode"`
	Events      int      `json:"events"`
	Duplicates  int      `json:"duplicates"`
	LostSamples uint64   `json:"lost_samples"`
	Stages      []string `json:"stages"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "net_rx_tracing: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config{}
	flag.StringVar(&cfg.bpfDir, "bpf-dir", "_output/bpf", "directory holding the BPF objects")
	flag.StringVar(&cfg.mode, "mode", modeAuto, "auto, fentry or kprobe")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Second, "how long to wait for events")
	flag.IntVar(&cfg.maxEvents, "events", 512, "stop after this many events")
	flag.Int64Var(&cfg.thresholdNS, "threshold-ns", fixtureThresholdNS, "latency threshold in nanoseconds")
	flag.Parse()

	switch cfg.mode {
	case modeAuto, modeFentry, modeKprobe:
	default:
		return fmt.Errorf("unknown mode %q", cfg.mode)
	}

	// The loader logs the entry point it selected; keep that on stderr so the
	// events on stdout stay a readable JSON stream.
	log.SetOutput(os.Stderr)

	if err := bpf.Init(nil); err != nil {
		return fmt.Errorf("initialize BPF: %w", err)
	}
	defer bpf.Shutdown()

	bpf.DefaultObjDir = cfg.bpfDir

	// The tracer reads skb->tstamp, which the kernel only records while
	// software RX timestamping is enabled somewhere in the system. The daemon
	// does the same before it loads the object; the fixture cannot rely on it
	// running.
	timestampFD, err := enableSkbTimestamp()
	if err != nil {
		return err
	}
	defer unix.Close(timestampFD)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	object, reader, err := startTracing(ctx, cfg)
	if err != nil {
		return err
	}
	defer object.Close()
	defer reader.Close()

	entryPoint, err := selectedEntryPoint(object)
	if err != nil {
		return err
	}
	if cfg.mode == modeFentry && entryPoint != fentryProgram {
		return fmt.Errorf("mode %s requires the fentry entry point, kernel selected %s", cfg.mode, entryPoint)
	}
	if cfg.mode == modeKprobe && entryPoint != kprobeProgram {
		return fmt.Errorf("mode %s requires the kprobe entry point, kernel selected %s", cfg.mode, entryPoint)
	}

	// The hooks are attached by now: announce it before the traffic a caller
	// may be timing arrives.
	if err := json.NewEncoder(os.Stdout).Encode(readyMarker{
		Ready:      true,
		Mode:       cfg.mode,
		EntryPoint: entryPoint,
	}); err != nil {
		return fmt.Errorf("write ready marker: %w", err)
	}

	observed, err := collectEvents(reader, entryPoint, cfg)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(os.Stdout).Encode(summary{
		Summary:     true,
		EntryPoint:  entryPoint,
		Mode:        cfg.mode,
		Events:      observed.events,
		Duplicates:  observed.duplicates,
		LostSamples: observed.lost,
		Stages:      observed.stages,
	}); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	if observed.events == 0 {
		return fmt.Errorf("no net_rx_latency event arrived within %s", cfg.timeout)
	}
	if observed.duplicates != 0 {
		return fmt.Errorf("%d events repeated a packet, expected one per hook", observed.duplicates)
	}

	return nil
}

// enableSkbTimestamp turns on software RX timestamps system wide, the
// prerequisite of every stage this tracer reports. It mirrors the socket the
// daemon opens before loading the object.
func enableSkbTimestamp() (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return -1, fmt.Errorf("create timestamp socket: %w", err)
	}

	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, unix.SOF_TIMESTAMPING_RX_SOFTWARE); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("enable skb rx timestamp: %w", err)
	}

	return fd, nil
}

// startTracing loads and attaches the object the mode asks for. The returned
// object is already attached and the reader already created: the fixture must
// not attach again.
func startTracing(ctx context.Context, cfg config) (bpf.BPF, bpf.PerfEventReader, error) {
	consts := map[string]any{
		"mono_wall_offset":      int64(0),
		"rxlat_thresh_netif":    cfg.thresholdNS,
		"rxlat_thresh_tcpv4":    cfg.thresholdNS,
		"rxlat_thresh_usercopy": cfg.thresholdNS,
	}

	if cfg.mode == modeKprobe {
		object, err := bpf.LoadBPF(kprobeObject, consts)
		if err != nil {
			return nil, nil, err
		}

		reader, err := object.AttachAndEventPipe(ctx, eventMap, bpf.DefaultPerfEventBufferBytes)
		if err != nil {
			return nil, nil, errors.Join(err, object.Close())
		}

		return object, reader, nil
	}

	return bpf.LoadAttachAndEventPipeWithFallback(
		ctx,
		fentryObject,
		consts,
		[]bpf.TracingVariantPair{{
			Kprobe: kprobeProgram,
			Fentry: fentryProgram,
			Target: "tcp_v4_rcv",
		}},
		eventMap,
		bpf.DefaultPerfEventBufferBytes,
	)
}

// selectedEntryPoint reports which entry point of tcp_v4_rcv the object holds.
func selectedEntryPoint(object bpf.BPF) (string, error) {
	info, err := object.Info()
	if err != nil {
		return "", err
	}

	for _, program := range info.ProgramsInfo {
		if program.Name == kprobeProgram || program.Name == fentryProgram {
			return program.Name, nil
		}
	}

	return "", errors.New("object holds no tcp_v4_rcv entry point")
}

// collection is what one fixture run observed.
type collection struct {
	events     int
	duplicates int
	lost       uint64
	stages     []string
}

// collectEvents prints every event and counts the ones that repeat a packet.
func collectEvents(
	reader bpf.PerfEventReader,
	entryPoint string,
	cfg config,
) (collection, error) {
	deadline := time.Now().Add(cfg.timeout)
	encoder := json.NewEncoder(os.Stdout)

	var (
		observed  collection
		seenStage = make(map[string]bool)
		seenEvent = make(map[string]bool)
	)

	for observed.events < cfg.maxEvents && time.Now().Before(deadline) {
		// ReadBatch returns what has arrived within a fixed window, so the
		// fixture stays responsive when no event arrives at all.
		batch, err := reader.ReadBatch(func() any { return new(abi.NetRXLatencyEvent) })
		if err != nil {
			return collection{}, fmt.Errorf("read perf events: %w", err)
		}

		observed.lost += batch.LostSamples

		for _, raw := range batch.Events {
			event, ok := raw.(*abi.NetRXLatencyEvent)
			if !ok {
				return collection{}, fmt.Errorf("unexpected event type %T", raw)
			}

			if err := recordEvent(encoder, event, entryPoint, seenStage, seenEvent, &observed); err != nil {
				return collection{}, err
			}

			observed.events++
		}
	}

	return observed, nil
}

// recordEvent prints one event and tracks the stages and repeated packets seen
// so far.
func recordEvent(
	encoder *json.Encoder,
	event *abi.NetRXLatencyEvent,
	entryPoint string,
	seenStage map[string]bool,
	seenEvent map[string]bool,
	observed *collection,
) error {
	if int(event.LatencyStage) >= len(stageNames) {
		return fmt.Errorf("unknown latency stage %d", event.LatencyStage)
	}

	stage := stageNames[event.LatencyStage]
	if !seenStage[stage] {
		seenStage[stage] = true
		observed.stages = append(observed.stages, stage)
	}

	// One hook produces one event per packet: a repeated key means the hook
	// was attached twice.
	key := fmt.Sprintf("%s/%d/%d/%d/%d",
		stage, event.TCPSeq, netutil.Ntohs(event.TCPSport), netutil.Ntohs(event.TCPDport), event.LatencyNS)
	duplicate := seenEvent[key]
	if duplicate {
		observed.duplicates++
	}
	seenEvent[key] = true

	err := encoder.Encode(eventRecord{
		Event:      "net_rx_latency",
		Stage:      stage,
		LatencyNS:  event.LatencyNS,
		Saddr:      netutil.Inetv4Ntop(event.TCPSaddr).String(),
		Sport:      netutil.Ntohs(event.TCPSport),
		Daddr:      netutil.Inetv4Ntop(event.TCPDaddr).String(),
		Dport:      netutil.Ntohs(event.TCPDport),
		Seq:        event.TCPSeq,
		PacketLen:  event.PacketLenBytes,
		Duplicate:  duplicate,
		EntryPoint: entryPoint,
	})
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}

// Copyright 2025, 2026 The HuaTuo Authors
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
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
	"github.com/ccfos/huatuo/internal/version"
)

const perfToolName = "perf"

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/perf.c -o $BPF_DIR/perf.o

func mainAction(ctx *cli.Context) error {
	bpfPath := ctx.String("bpf-path")
	optPid := ctx.Uint64("pid")
	optDuration := ctx.Int("duration")

	var cgroupCSSRef pod.CgroupCSSRef
	if containerID := ctx.String("container-id"); containerID != "" {
		var err error
		cgroupCSSRef, err = resolvePerfContainerCSSRef(ctx.Context, ctx.String("huatuo-api-address"), containerID)
		if err != nil {
			return err
		}
	}

	if err := bpf.Init(&bpf.Option{
		KeepaliveTimeout: optDuration,
	}); err != nil {
		return fmt.Errorf("init bpf err %w", err)
	}
	defer bpf.Shutdown()

	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return fmt.Errorf("read bpf object: %w", err)
	}

	b, err := bpf.LoadBPFFromBytes(bpfPath, bpfBytes, map[string]any{
		"perf_filter_css_addr":  cgroupCSSRef.CSSAddr,
		"perf_filter_cgroup_id": cgroupCSSRef.CgroupID,
		"pid":                   optPid,
	})
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	opt := bpf.AttachOption{
		ProgramName: "perf_event_sw_cpu_clock",
	}
	opt.PerfEvent.SampleFreq = 99
	opt.PerfEvent.Type = unix.PERF_TYPE_SOFTWARE
	opt.PerfEvent.Config = unix.PERF_COUNT_SW_CPU_CLOCK
	if err := b.AttachWithOptions([]bpf.AttachOption{opt}); err != nil {
		return fmt.Errorf("attach err %w", err)
	}

	signalWait := make(chan os.Signal, 1)
	signal.Notify(signalWait, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-time.After(time.Duration(optDuration) * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("caller requests stop")
	case sig := <-signalWait:
		return fmt.Errorf("received signal %s", sig)
	}

	flameData, err := buildFlameData(b)
	if err != nil {
		return fmt.Errorf("parsedata err %w", err)
	}
	if ctx.Bool("tui") {
		return flamegraphtui.Run(flameData)
	}
	if err := writeFlameDataJSON(os.Stdout, flameData); err != nil {
		return fmt.Errorf("write flame data: %w", err)
	}

	return nil
}

func resolvePerfContainerCSSRef(ctx context.Context, serverAddress, containerID string) (pod.CgroupCSSRef, error) {
	nodeClient, err := client.NewNode(&client.NodeConfig{})
	if err != nil {
		return pod.CgroupCSSRef{}, fmt.Errorf("initialize node client: %w", err)
	}
	container, err := nodeClient.FetchContainer(ctx, client.NodeAddress{HostPort: serverAddress}, containerID)
	if err != nil {
		if !perfLocalNodeAddress(serverAddress) {
			return pod.CgroupCSSRef{}, fmt.Errorf("fetch container %q metadata: %w", containerID, err)
		}
		return pod.ContainerCSSBySubsys(containerID, "cpu")
	}
	if container.CgroupCSSRefs != nil {
		if remoteRef, found := (*container.CgroupCSSRefs)["cpu"]; found {
			cssAddr, cssOK := kernaddr.Parse(remoteRef.CSSAddr)
			cgroupID, idOK := kernaddr.Parse(remoteRef.CgroupID)
			if !cssOK || !idOK || cssAddr == 0 || cgroupID == 0 {
				return pod.CgroupCSSRef{}, fmt.Errorf("container %q has invalid CPU CSS identity in node API", containerID)
			}
			if legacyAddr, ok := kernaddr.Parse(container.CgroupCSS["cpu"]); ok && legacyAddr != cssAddr {
				return pod.CgroupCSSRef{}, fmt.Errorf("container %q has conflicting CPU CSS addresses in node API", containerID)
			}
			return pod.CgroupCSSRef{CSSAddr: cssAddr, CgroupID: cgroupID}, nil
		}
	}
	if !perfLocalNodeAddress(serverAddress) {
		return pod.CgroupCSSRef{}, fmt.Errorf("container %q has no CPU kernfs ID in remote node API", containerID)
	}
	local, err := pod.ContainerCSSBySubsys(containerID, "cpu")
	if err != nil {
		return pod.CgroupCSSRef{}, fmt.Errorf("resolve container %q CPU CSS identity locally: %w", containerID, err)
	}
	legacyAddr, ok := kernaddr.Parse(container.CgroupCSS["cpu"])
	if !ok || legacyAddr != local.CSSAddr {
		return pod.CgroupCSSRef{}, fmt.Errorf("container %q CPU CSS address differs between local cache and node API", containerID)
	}
	return local, nil
}

func perfLocalNodeAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func main() {
	app := cli.NewApp()
	app.Name = perfToolName
	app.Usage = "eBPF on-CPU sampling profiler for Linux, scoped by PID or container"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "bpf-path",
			Value: "bpf/perf.o",
			Usage: "path to the perf BPF object file",
		},
		&cli.StringFlag{
			Name:  "container-id",
			Value: "",
			Usage: "Container's ID",
		},
		&cli.Uint64Flag{
			Name:  "pid",
			Value: 0,
			Usage: "Task pid number",
		},
		&cli.IntFlag{
			Name:  "duration",
			Value: 5,
			Usage: "Tool duration(s)",
		},
		&cli.StringFlag{
			Name:  "huatuo-api-address",
			Value: "127.0.0.1:19704",
			Usage: "HuaTuo API address used to resolve container metadata",
		},
		&cli.BoolFlag{
			Name:  "tui",
			Usage: "open an interactive terminal flamegraph viewer instead of printing JSON",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		log.SetOutput(io.Discard)
		return nil
	}

	app.Action = mainAction

	version.Wire(app, version.Seed{
		Name:      perfToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "perf: %v\n", err)
		os.Exit(1)
	}
}

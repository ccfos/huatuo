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

package events

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"syscall"
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pcapfilter"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

type tcpRetransmitTracing struct{}

const (
	tcpRetransmitTracerName = "tcp_retransmit"
	tcpSharkToolName        = "tcpshark"
	tcpSharkStopTimeout     = 7 * time.Second
)

func init() {
	tracing.RegisterEventTracing(tcpRetransmitTracerName, newTCPRetransmit)
	toolstream.RegisterDefault[*types.TCPRetransmitTracing](tcpSharkToolName, handleTCPRetransmitEvent)
}

func newTCPRetransmit() (*tracing.EventTracingAttr, error) {
	if err := validateTCPRetransmitFilter(configSnapshot()); err != nil {
		return nil, err
	}
	return &tracing.EventTracingAttr{
		TracingData: &tcpRetransmitTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func validateTCPRetransmitFilter(config *Config) error {
	if !config.TCPRetransmit.EnableDropwatchCorrelation {
		return nil
	}
	if err := pcapfilter.ValidateL3Compatible(effectiveTCPRetransmitFilter(config)); err != nil {
		return fmt.Errorf(
			"EventTracing.TCPRetransmit.Filter is incompatible with local correlation: %w",
			err,
		)
	}
	return nil
}

// Start launches tcpshark in retransmit mode and waits for it to finish.
// Events are received via the default toolstream server registered in init.
func (c *tcpRetransmitTracing) Start(ctx context.Context) error {
	config := configSnapshot()
	cmd := exec.Command(
		path.Join(internalconfig.CoreBinDir, tcpSharkToolName),
		tcpRetransmitArgs(config)...,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("start tcpshark: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("start tcpshark: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tcpshark: %w", err)
	}
	go logSubprocessOutput("tcpshark", stdout)
	go logSubprocessOutput("tcpshark", stderr)

	log.Infof("tcpshark started pid=%d", cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		if err := stopTCPShark(cmd, done); err != nil {
			return err
		}
		log.Info("tcpshark stopped")
		return nil
	case err := <-done:
		if err != nil {
			return fmt.Errorf("tcpshark exited: %w", err)
		}
		log.Info("tcpshark exited")
		return nil
	}
}

func tcpRetransmitArgs(config *Config) []string {
	args := []string{
		"--mode", "retransmit",
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "tcp_retransmit.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--max-events-per-second", strconv.FormatUint(config.TCPRetransmit.MaxEventsPerSecond, 10),
		"--source-types", toolstream.SourceTypeEvent,
	}
	if filter := effectiveTCPRetransmitFilter(config); filter != "" {
		args = append(args, "--filter", filter)
	}
	if config.TCPRetransmit.EnableDropwatchCorrelation {
		args = append(args,
			"--dropwatch-correlation", "local",
			"--dropwatch-bpf-path", path.Join(internalconfig.CoreBpfDir, "dropwatch.o"),
			"--dropwatch-max-events-per-second",
			strconv.FormatUint(config.Dropwatch.MaxEventsPerSecond, 10),
		)
	}
	if config.TCPRetransmit.EnableTLP {
		args = append(args, "--enable-tlp")
	}
	return args
}

func logSubprocessOutput(name string, output io.Reader) {
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		log.Warnf("%s: %s", name, scanner.Text())
	}
}

func stopTCPShark(cmd *exec.Cmd, done <-chan error) error {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		killErr := cmd.Process.Kill()
		waitErr := <-done
		return errors.Join(
			fmt.Errorf("stop tcpshark: signal: %w", err),
			wrapTCPSharkStopError("force kill", killErr),
			wrapTCPSharkStopError("wait", waitErr),
		)
	}

	timer := time.NewTimer(tcpSharkStopTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return wrapTCPSharkStopError("wait", err)
	case <-timer.C:
		killErr := cmd.Process.Kill()
		waitErr := <-done
		return errors.Join(
			fmt.Errorf("stop tcpshark: graceful timeout after %s", tcpSharkStopTimeout),
			wrapTCPSharkStopError("force kill", killErr),
			wrapTCPSharkStopError("wait", waitErr),
		)
	}
}

func wrapTCPSharkStopError(operation string, err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return fmt.Errorf("stop tcpshark: %s: %w", operation, err)
}

func handleTCPRetransmitEvent(_ *toolstream.Session, ev *types.TCPRetransmitTracing) error {
	if ev.ContainerID == "" {
		ev.ContainerID = pod.ContainerIDByCgroupNetNS(pod.ContainerCgroupNetNS{
			MemoryCgroupCSSAddr: kernaddr.ParseOrZero(ev.MemoryCgroupCSSAddr),
			NetNamespaceCookie:  ev.NetNamespaceCookie,
			NetNamespaceInum:    uint64(ev.NetNamespaceInum),
		})
	}

	return tracing.Save(&tracing.WriteRequest{
		TracerName:  tcpRetransmitTracerName,
		ContainerID: ev.ContainerID,
		TracerTime:  time.Now(),
		TracerData:  ev,
	})
}

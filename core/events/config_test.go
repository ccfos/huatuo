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
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	testutils "github.com/ccfos/huatuo/internal/testing"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "valid config",
		},
		{
			name: "zero scheduler tick threshold",
			configure: func(cfg *Config) {
				cfg.SchedTick.IntervalThreshold = 0
			},
			wantError: "scheduler tick interval threshold must be greater than zero",
		},
		{
			name: "invalid issues list",
			configure: func(cfg *Config) {
				cfg.IssuesList = [][]string{{"missing-expression"}}
			},
			wantError: "validating issues list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.SchedTick.IntervalThreshold = 1
			if tt.configure != nil {
				tt.configure(cfg)
			}

			err := cfg.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func TestConfigCloneDoesNotShareMutableReferences(t *testing.T) {
	source := &Config{}
	testutils.PopulateCloneSource(t, source)

	testutils.AssertDeepClone(t, source, source.Clone())
}

func TestSetPublishesIndependentConfig(t *testing.T) {
	src := &Config{IssuesList: [][]string{{"dropwatch", "kfree_skb"}}}
	src.Netdev.DeviceList = []string{"eth0"}
	Set(src)
	src.IssuesList[0][0] = "net_rx_latency"
	src.Netdev.DeviceList[0] = "eth1"

	snapshot := configSnapshot()
	if snapshot.IssuesList[0][0] != "dropwatch" || snapshot.Netdev.DeviceList[0] != "eth0" {
		t.Fatalf("published config aliases caller data: %+v", snapshot)
	}
}

func TestSetPublishesConsistentSnapshots(t *testing.T) {
	pairs := [][2]uint64{{3, 300}, {4, 400}}
	Set(&Config{})
	valid := map[[2]uint64]bool{{0, 0}: true, pairs[0]: true, pairs[1]: true}
	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		wg.Add(1)
		go func(pair [2]uint64) {
			defer wg.Done()
			<-start
			for range 200 {
				cfg := &Config{}
				cfg.NetRxLatency.Driver2NetRx = pair[0]
				cfg.NetRxLatency.Driver2TCP = pair[1]
				Set(cfg)
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				cfg := configSnapshot()
				got := [2]uint64{cfg.NetRxLatency.Driver2NetRx, cfg.NetRxLatency.Driver2TCP}
				if !valid[got] {
					select {
					case errCh <- fmt.Errorf("observed mixed config snapshot: %v", got):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestLocalCorrelationDoesNotUseStandaloneDropwatchFilter(t *testing.T) {
	config := &Config{}
	config.Dropwatch.Filter = " tcp and port 443 "
	config.TCPRetransmit.EnableDropwatchCorrelation = true
	config.TCPRetransmit.Filter = " tcp and port 80 "

	retransmitFilter := flagArgument(t, tcpRetransmitArgs(config), "--filter")
	if retransmitFilter != "tcp and port 80" {
		t.Fatalf("local correlation filter = %q, want %q", retransmitFilter, "tcp and port 80")
	}
}

func TestTCPRetransmitFilterSelection(t *testing.T) {
	tests := []struct {
		name        string
		correlation bool
		tcpFilter   string
		dropFilter  string
		wantFilter  string
		wantPresent bool
	}{
		{name: "off without filter"},
		{
			name: "off uses independent filter", tcpFilter: " tcp port 80 ",
			wantFilter: "tcp port 80", wantPresent: true,
		},
		{
			name: "local uses retransmit filter", correlation: true,
			tcpFilter: " tcp port 443 ", dropFilter: "udp port 53",
			wantFilter: "tcp port 443", wantPresent: true,
		},
		{
			name: "local defaults to TCP", correlation: true,
			dropFilter: "udp port 53",
			wantFilter: "tcp", wantPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{}
			config.TCPRetransmit.EnableDropwatchCorrelation = tt.correlation
			config.TCPRetransmit.Filter = tt.tcpFilter
			config.Dropwatch.Filter = tt.dropFilter
			got, present := findFlagArgument(tcpRetransmitArgs(config), "--filter")
			if present != tt.wantPresent || got != tt.wantFilter {
				t.Fatalf(
					"--filter = (%q, %t), want (%q, %t)",
					got,
					present,
					tt.wantFilter,
					tt.wantPresent,
				)
			}
		})
	}
}

func TestNewTCPRetransmitAllowsIndependentDropwatchFilter(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	config := &Config{}
	config.TCPRetransmit.EnableDropwatchCorrelation = true
	config.TCPRetransmit.Filter = "tcp port 80"
	config.Dropwatch.Filter = "tcp port 443"
	Set(config)

	if _, err := newTCPRetransmit(); err != nil {
		t.Fatalf("newTCPRetransmit() error = %v", err)
	}
}

func TestNewTCPRetransmitRejectsL2LocalFilter(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	config := &Config{}
	config.TCPRetransmit.EnableDropwatchCorrelation = true
	config.TCPRetransmit.Filter = "ether host 02:00:00:00:00:01"
	Set(config)

	if _, err := newTCPRetransmit(); err == nil || !strings.Contains(
		err.Error(),
		"filter requires ethernet header fields unavailable to local correlation",
	) {
		t.Fatalf("newTCPRetransmit() error = %v, want L3 compatibility error", err)
	}
}

func TestTCPRetransmitArgsDropwatchCorrelation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := &Config{}
		config.TCPRetransmit.Filter = "tcp and port 443"
		config.TCPRetransmit.MaxEventsPerSecond = 321
		config.TCPRetransmit.EnableDropwatchCorrelation = enabled
		args := tcpRetransmitArgs(config)
		if got := slices.Contains(args, "--with-dropwatch"); got != enabled {
			t.Fatalf("enabled=%t: --with-dropwatch present = %t", enabled, got)
		}
		if enabled {
			if got := flagArgument(t, args, "--bpf-path-dir"); got != internalconfig.CoreBpfDir {
				t.Fatalf("--bpf-path-dir = %q, want %q", got, internalconfig.CoreBpfDir)
			}
			if slices.Contains(args, "--bpf-path") {
				t.Fatalf("correlation args contain --bpf-path: %v", args)
			}
		} else {
			wantPath := path.Join(internalconfig.CoreBpfDir, "tcp_retransmit.o")
			if got := flagArgument(t, args, "--bpf-path"); got != wantPath {
				t.Fatalf("--bpf-path = %q, want %q", got, wantPath)
			}
			if slices.Contains(args, "--bpf-path-dir") {
				t.Fatalf("non-correlation args contain --bpf-path-dir: %v", args)
			}
		}
		if got := flagArgument(t, args, "--max-events-per-second"); got != "321" {
			t.Fatalf("shared tcpshark rate limit = %q, want 321", got)
		}
	}
}

func flagArgument(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not found in %v", flag, args)
	return ""
}

func findFlagArgument(args []string, flag string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

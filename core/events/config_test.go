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
	"slices"
	"strings"
	"sync"
	"testing"

	testutils "huatuo-bamai/internal/testing"
)

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
				config := &Config{}
				config.NetRxLatency.Driver2NetRx = pair[0]
				config.NetRxLatency.Driver2TCP = pair[1]
				Set(config)
			}
		}(pair)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1_000 {
				config := configSnapshot()
				got := [2]uint64{
					config.NetRxLatency.Driver2NetRx,
					config.NetRxLatency.Driver2TCP,
				}
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

func TestEffectiveDropwatchFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   string
	}{
		{name: "empty", want: "tcp"},
		{name: "whitespace", filter: "  ", want: "tcp"},
		{name: "custom", filter: " tcp and port 443 ", want: "tcp and port 443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{}
			config.Dropwatch.Filter = tt.filter
			if got := effectiveDropwatchFilter(config); got != tt.want {
				t.Fatalf("effectiveDropwatchFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalCorrelationDoesNotUseStandaloneDropwatchFilter(t *testing.T) {
	config := &Config{}
	config.Dropwatch.Filter = " tcp and port 443 "
	config.TCPRetransmit.EnableDropwatchCorrelation = true
	config.TCPRetransmit.Filter = " tcp and port 80 "

	dropwatchFilter := flagArgument(t, dropwatchArgs(config), "--filter")
	retransmitFilter := flagArgument(t, tcpRetransmitArgs(config), "--filter")
	if dropwatchFilter != "tcp and port 443" {
		t.Fatalf("standalone dropwatch filter = %q, want %q", dropwatchFilter, "tcp and port 443")
	}
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
		config.Dropwatch.MaxEventsPerSecond = 321
		config.TCPRetransmit.EnableDropwatchCorrelation = enabled
		args := tcpRetransmitArgs(config)
		for _, flag := range []string{
			"--dropwatch-correlation",
			"--dropwatch-bpf-path",
			"--dropwatch-max-events-per-second",
		} {
			if got := slices.Contains(args, flag); got != enabled {
				t.Fatalf("enabled=%t: %s present = %t", enabled, flag, got)
			}
		}
		if enabled {
			if got := flagArgument(t, args, "--dropwatch-correlation"); got != "local" {
				t.Fatalf("correlation mode = %q, want local", got)
			}
			if got := flagArgument(t, args, "--dropwatch-max-events-per-second"); got != "321" {
				t.Fatalf("dropwatch rate limit = %q, want 321", got)
			}
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

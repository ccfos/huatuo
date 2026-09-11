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

package dropwatch

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{"empty", Config{}, false},
		{"automatic", Config{BPFPath: "net_dropwatch.o"}, true},
		{"software", Config{BPFPath: "net_dropwatch.o", HardwareMode: HardwareDisabled}, true},
		{"invalid hardware", Config{BPFPath: "net_dropwatch.o", HardwareMode: 255}, false},
		{"conflicting devices", Config{BPFPath: "net_dropwatch.o", IncludeDevices: []string{"lo"}, ExcludeDevices: []string{"lo"}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.cfg.validate(); (err == nil) != test.valid {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestOpenFailureBeforeAttach(t *testing.T) {
	cfg := Config{BPFPath: t.TempDir() + "/missing.o", HardwareMode: HardwareDisabled}
	if tracer, err := Open(t.Context(), &cfg); err == nil || tracer != nil {
		t.Fatalf("missing object = %v, %v", tracer, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if tracer, err := Open(ctx, &cfg); !errors.Is(err, context.Canceled) || tracer != nil {
		t.Fatalf("canceled = %v, %v", tracer, err)
	}
}

func TestResolveNetdevOptions(t *testing.T) {
	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	included := []string{" lo ", ""}
	got, err := resolveNetdevOptions(included, nil)
	if err != nil || got.mode != netdevModeAllow || len(got.ifindexes) != 1 || got.ifindexes[0] != uint32(iface.Index) {
		t.Fatalf("include = %+v, %v", got, err)
	}
	included[0] = "changed"
	if got.ifindexes[0] != uint32(iface.Index) {
		t.Fatal("filter retained caller data")
	}
	got, err = resolveNetdevOptions(nil, []string{"lo"})
	if err != nil || got.mode != netdevModeDeny {
		t.Fatalf("exclude = %+v, %v", got, err)
	}
	if _, err := resolveNetdevOptions([]string{" "}, nil); err == nil {
		t.Fatal("empty names accepted")
	}
	if _, err := resolveNetdevOptions([]string{"dropwatch-nonexistent-interface"}, nil); err == nil {
		t.Fatal("missing interface accepted")
	}
}

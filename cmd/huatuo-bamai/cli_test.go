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
	"flag"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/bpf"
)

func TestParseBPFAttachOverride(t *testing.T) {
	want := bpf.AttachOverride{
		ObjectName:  "memory_oom_kill.o",
		ProgramName: "oom_kill_process",
		Symbol:      "oom_kill_process_mock",
	}
	got, err := parseBPFAttachOverride("memory_oom_kill.o:oom_kill_process=oom_kill_process_mock")
	if err != nil {
		t.Fatalf("parseBPFAttachOverride() error = %v", err)
	}
	if got != want {
		t.Fatalf("parseBPFAttachOverride() = %+v, want %+v", got, want)
	}

	for _, value := range []string{"", "memory_oom_kill.o:oom_kill_process", "memory_oom_kill.o:=mock", ":oom_kill_process=mock"} {
		if _, err := parseBPFAttachOverride(value); err == nil {
			t.Errorf("parseBPFAttachOverride(%q) succeeded, want error", value)
		}
	}
}

func TestOptionsFromContextEnablesCgroup(t *testing.T) {
	app := cli.NewApp()
	opts := &Options{}
	opts.AddFlags(app)
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, cliFlag := range app.Flags {
		if err := cliFlag.Apply(flags); err != nil {
			t.Fatalf("apply flag: %v", err)
		}
	}
	if err := flags.Parse([]string{"--region", "test", "--enable-cgroup"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	if err := opts.FromContext(cli.NewContext(app, flags, nil)); err != nil {
		t.Fatalf("FromContext() error = %v", err)
	}
	if !opts.EnableCgroup {
		t.Fatal("EnableCgroup = false, want true")
	}
}

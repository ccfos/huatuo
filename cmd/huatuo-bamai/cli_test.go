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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	internalconfig "github.com/ccfos/huatuo/internal/config"

	"github.com/urfave/cli/v2"
)

func TestOptionsFromContext(t *testing.T) {
	for _, mock := range []bool{false, true} {
		app := cli.NewApp()
		opts := &Options{}
		opts.AddFlags(app)
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		for _, cliFlag := range app.Flags {
			if err := cliFlag.Apply(flags); err != nil {
				t.Fatalf("apply flag: %v", err)
			}
		}
		args := []string{"--region", "test", "--enable-cgroup"}
		if mock {
			args = append(args, "--bpf-mock", "--bpf-mock-object", "memory_oom_kill.o", "--bpf-mock-object", "system_softlockup.o")
		}
		if err := flags.Parse(args); err != nil {
			t.Fatalf("parse flags: %v", err)
		}

		if err := opts.FromContext(cli.NewContext(app, flags, nil)); err != nil {
			t.Fatalf("FromContext() error = %v", err)
		}
		if opts.BPFMock != mock {
			t.Fatalf("BPFMock = %t, want %t", opts.BPFMock, mock)
		}
		if mock && (len(opts.BPFMockObjects) != 2 || opts.BPFMockObjects[0] != "memory_oom_kill.o" || opts.BPFMockObjects[1] != "system_softlockup.o") {
			t.Fatalf("BPFMockObjects = %v, want the selected objects", opts.BPFMockObjects)
		}
		if !opts.EnableCgroup {
			t.Fatal("EnableCgroup = false, want true")
		}
	}
}

func TestMockObjectRequiresMockMode(t *testing.T) {
	app := cli.NewApp()
	opts := &Options{}
	opts.AddFlags(app)
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, cliFlag := range app.Flags {
		if err := cliFlag.Apply(flags); err != nil {
			t.Fatal(err)
		}
	}
	if err := flags.Parse([]string{"--bpf-mock-object", "memory_oom_kill.o"}); err != nil {
		t.Fatal(err)
	}
	if err := opts.FromContext(cli.NewContext(app, flags, nil)); err == nil {
		t.Fatal("mock object selection without mock mode was accepted")
	}
}

func TestConfigureRuntimeAPIAddress(t *testing.T) {
	// configureRuntime publishes process-global configuration and retains its path.
	const childEnv = "HUATUO_TEST_CONFIGURE_RUNTIME"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureRuntimeAPIAddress$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("configuration subprocess failed: %v\n%s", err, output)
		}

		return
	}

	tests := []struct {
		listen string
		want   string
	}{
		{
			listen: ":19704",
			want:   "127.0.0.1:19704",
		},
		{
			listen: "127.0.0.1:29704",
			want:   "127.0.0.1:29704",
		},
		{
			listen: ":29704",
			want:   "127.0.0.1:29704",
		},
		{
			listen: "0.0.0.0:29704",
			want:   "127.0.0.1:29704",
		},
		{
			listen: "[::]:29704",
			want:   "127.0.0.1:29704",
		},
	}
	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			opts := &Options{
				ConfigDir:  t.TempDir(),
				ConfigFile: "bamai.conf",
				BPFObjDir:  bpf.DefaultObjDir,
				ToolBinDir: internalconfig.CoreBinDir,
			}
			content := fmt.Sprintf(`[HTTPServer]
ListenAddress = %q

[HTTPServer.Auth]
BearerToken = "test-node-token"
`, tt.listen)
			if err := os.WriteFile(filepath.Join(opts.ConfigDir, opts.ConfigFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			internalconfig.CoreAPIAddress = ""
			if err := configureRuntime(opts); err != nil {
				t.Fatalf("configureRuntime() error = %v", err)
			}

			if got := internalconfig.CoreAPIAddress; got != tt.want {
				t.Fatalf("CoreAPIAddress = %q, want %q", got, tt.want)
			}
		})
	}
}

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
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	internalconfig "github.com/ccfos/huatuo/internal/config"

	"github.com/urfave/cli/v2"
)

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

// TestWorkloadDiscoveryFlags 验证统一开关与废弃别名解析为同一选项，默认启用发现，
// 显式 false 不禁用发现，帮助文本明确标注旧参数废弃。
func TestWorkloadDiscoveryFlags(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		disabled bool
	}{
		{name: "default"},
		{name: "workload", args: []string{"--disable-workload-discovery"}, disabled: true},
		{name: "legacy", args: []string{"--disable-kubelet"}, disabled: true},
		{name: "explicit false", args: []string{"--disable-workload-discovery=false"}},
		{name: "legacy false", args: []string{"--disable-kubelet=false"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := &Options{}
			app := cli.NewApp()
			opts.AddFlags(app)
			app.Action = opts.FromContext
			args := append([]string{"huatuo-bamai", "--region", "huatuo-region"}, test.args...)
			if err := app.Run(args); err != nil {
				t.Error(err)
				return
			}
			if opts.DisableWorkloadDiscovery != test.disabled {
				t.Errorf("disabled=%t, want %t", opts.DisableWorkloadDiscovery, test.disabled)
			}
		})
	}
	app := cli.NewApp()
	(&Options{}).AddFlags(app)
	var help bytes.Buffer
	app.Writer = &help
	if err := app.Run([]string{"huatuo-bamai", "--help"}); err != nil {
		t.Error(err)
	}
	if !strings.Contains(help.String(), "--disable-kubelet is deprecated") {
		t.Error("help did not deprecate the legacy discovery flag")
	}
}

// TestConfigureRuntimeDiscoveryConflict 验证统一禁用开关与 GuestBridge 冲突在启动
// 初始化前报错；禁用存储不能绕过冲突，禁用发现也不能绕过未知来源校验。
func TestConfigureRuntimeDiscoveryConflict(t *testing.T) {
	const childEnv = "HUATUO_TEST_DISCOVERY_CONFLICT"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigureRuntimeDiscoveryConflict$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("configuration subprocess failed: %v\n%s", err, output)
		}
		return
	}
	for _, test := range []struct {
		name           string
		body           string
		disableStorage bool
		want           string
	}{
		{name: "bridge", body: "[GuestBridge]\nEnabled=true", want: "GuestBridge requires workload discovery"},
		{name: "bridge without storage", body: "[GuestBridge]\nEnabled=true", disableStorage: true, want: "GuestBridge requires workload discovery"},
		{name: "unsupported source", body: "[Pod]\nSource=\"cubesandbox\"", want: "unsupported Pod.Source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := &Options{ConfigDir: t.TempDir(), ConfigFile: "discovery.conf", DisableWorkloadDiscovery: true, DisableStorage: test.disableStorage}
			body := test.body + "\n[HTTPServer.Auth]\nBearerToken=\"discovery-test-token\"\n"
			if err := os.WriteFile(filepath.Join(opts.ConfigDir, opts.ConfigFile), []byte(body), 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := configureRuntime(opts); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("configureRuntime()=%v, want %q", err, test.want)
			}
		})
	}
}

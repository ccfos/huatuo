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
	"os"
	"path/filepath"
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

func TestConfigureRuntimePropagatesBpfObjDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "huatuo-bamai.conf"), []byte(`
[HTTPServer.Auth]
BearerToken = "test-node-secret"
`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	opts := &Options{
		ConfigDir:  tmpDir,
		ConfigFile: "huatuo-bamai.conf",
		BPFObjDir:  filepath.Join(tmpDir, "custom-bpf"),
		ToolBinDir: filepath.Join(tmpDir, "custom-bin"),
	}

	// configureRuntime mutates process-wide globals; snapshot and restore them
	// so this test is hermetic regardless of execution order.
	oldDefaultObjDir := bpf.DefaultObjDir
	oldCoreBinDir := internalconfig.CoreBinDir
	oldCoreBpfDir := internalconfig.CoreBpfDir
	t.Cleanup(func() {
		bpf.DefaultObjDir = oldDefaultObjDir
		internalconfig.CoreBinDir = oldCoreBinDir
		internalconfig.CoreBpfDir = oldCoreBpfDir
	})

	if err := configureRuntime(opts); err != nil {
		t.Fatalf("configureRuntime() error = %v", err)
	}

	if bpf.DefaultObjDir != opts.BPFObjDir {
		t.Fatalf("bpf.DefaultObjDir = %q, want %q", bpf.DefaultObjDir, opts.BPFObjDir)
	}
	if internalconfig.CoreBinDir != opts.ToolBinDir {
		t.Fatalf("internalconfig.CoreBinDir = %q, want %q", internalconfig.CoreBinDir, opts.ToolBinDir)
	}
	if internalconfig.CoreBpfDir != opts.BPFObjDir {
		t.Fatalf("internalconfig.CoreBpfDir = %q, want %q (--bpf-dir is not propagated to tool subprocesses)", internalconfig.CoreBpfDir, opts.BPFObjDir)
	}
}

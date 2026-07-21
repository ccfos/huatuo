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
)

func TestOptionsFromContextPreservesExplicitRelativeConfigDir(t *testing.T) {
	app := cli.NewApp()
	opts := &Options{}
	opts.AddFlags(app)
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, cliFlag := range app.Flags {
		if err := cliFlag.Apply(flags); err != nil {
			t.Fatalf("apply flag: %v", err)
		}
	}
	if err := flags.Parse([]string{
		"--config-dir", "relative-conf",
		"--enable-pprof",
		"--disable-cgroup",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	if err := opts.FromContext(cli.NewContext(app, flags, nil)); err != nil {
		t.Fatalf("FromContext() error = %v", err)
	}
	if opts.ConfigDir != "relative-conf" {
		t.Errorf("ConfigDir = %q, want %q", opts.ConfigDir, "relative-conf")
	}
	if !opts.EnablePProf {
		t.Error("EnablePProf = false, want true")
	}
	if !opts.DisableCgroup {
		t.Error("DisableCgroup = false, want true")
	}
}

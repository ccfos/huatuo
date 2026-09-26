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

func TestOptionsFromContextNodeIP(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "IPv4", value: "192.0.2.10", want: "192.0.2.10"},
		{name: "IPv6", value: "2001:0db8::1", want: "2001:db8::1"},
		{name: "invalid", value: "node-a", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := cli.NewApp()
			opts := &Options{}
			opts.AddFlags(app)
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			for _, cliFlag := range app.Flags {
				if err := cliFlag.Apply(flags); err != nil {
					t.Fatalf("apply flag: %v", err)
				}
			}
			if err := flags.Parse([]string{"--region", "test", "--node-ip", tt.value}); err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			err := opts.FromContext(cli.NewContext(app, flags, nil))
			if (err != nil) != tt.wantErr {
				t.Fatalf("FromContext() error = %v, wantErr %v", err, tt.wantErr)
			}
			if opts.NodeIP != tt.want && !tt.wantErr {
				t.Errorf("NodeIP = %q, want %q", opts.NodeIP, tt.want)
			}
		})
	}
}

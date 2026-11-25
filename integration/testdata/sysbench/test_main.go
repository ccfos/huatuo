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
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "sysbench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("sysbench", flag.ContinueOnError)
	cpuWait := flags.Bool("cpu-wait", false, "CPU scheduler wait reference: sample or percent")
	loadavg := flags.Bool("loadavg", false, "cgroup taskstats and task states: sample")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *cpuWait == *loadavg {
		return errors.New("select exactly one of --cpu-wait or --loadavg")
	}
	if *cpuWait {
		return runCPUWait(flags.Args())
	}
	return runDload(flags.Args())
}

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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type captureOptions struct {
	url       string
	directory string
	endpoints []string
	timeout   time.Duration
	maxBytes  int64
	pid       int
}

func main() {
	err := run(context.Background(), os.Args[1:])
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "pprof-tool: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected capture or summary; use --help")
	}

	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, "usage: pprof-tool <capture|summary> [flags]")
		return nil
	}

	if args[0] != "capture" && args[0] != "summary" {
		return fmt.Errorf("unknown command %q", args[0])
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opts captureOptions
	flags.Func("endpoint", "endpoint to capture (repeatable); summary selects captured names", func(value string) error {
		if value == "" {
			return errors.New("endpoint must not be empty")
		}

		opts.endpoints = append(opts.endpoints, value)
		return nil
	})
	if args[0] == "capture" {
		flags.StringVar(
			&opts.url,
			"url",
			"",
			"HTTP(S) base URL ending in /debug/pprof",
		)
		flags.StringVar(
			&opts.directory,
			"output-dir",
			"",
			"new capture directory (must not exist)",
		)
		flags.DurationVar(
			&opts.timeout,
			"timeout",
			10*time.Second,
			"complete HTTP request timeout",
		)
		flags.Int64Var(
			&opts.maxBytes,
			"max-bytes",
			64<<20,
			"maximum response bytes per endpoint",
		)
		flags.IntVar(
			&opts.pid,
			"pid",
			0,
			"optional local PID for command-line and process-lifetime checks",
		)
	} else {
		flags.StringVar(
			&opts.directory,
			"input-dir",
			"",
			"directory containing capture.json",
		)
	}

	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stdout, "usage: pprof-tool %s [flags]\n", args[0])
			flags.SetOutput(os.Stdout)
			flags.PrintDefaults()
		}

		return err
	}

	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	if opts.directory == "" {
		return errors.New("require an output-dir for capture or input-dir for summary")
	}

	if args[0] == "summary" {
		return summarize(opts.directory, opts.endpoints)
	}

	if opts.timeout <= 0 {
		return errors.New("timeout must be positive")
	}

	if opts.maxBytes <= 0 || opts.maxBytes == math.MaxInt64 {
		return errors.New("max-bytes must be positive and below MaxInt64")
	}

	if opts.pid < 0 {
		return errors.New("PID must be nonnegative")
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return capture(ctx, &opts)
}

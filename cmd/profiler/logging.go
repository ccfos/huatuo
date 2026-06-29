// Copyright 2025 The HuaTuo Authors
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
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"huatuo-bamai/internal/filerotate"
	"huatuo-bamai/internal/log"
)

const (
	rfc3339NanoFixed  = "2006-01-02T15:04:05.000000000Z07:00"
	defaultLogSizeMB  = 100
	profilerLogPrefix = "profiler"
)

// prefixFormatter prefixes each log line with "[profiler]" so the CLI output is
// distinguishable from any daemon logs that share the same stream.
type prefixFormatter struct {
	prefix    string
	formatter logrus.Formatter
}

func (f *prefixFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	entry.Message = fmt.Sprintf("[%s] %s", f.prefix, entry.Message)
	return f.formatter.Format(entry)
}

// loggingOptions holds the profiler CLI logging configuration.
type loggingOptions struct {
	verbose bool
	level   string
	file    string
	size    int
}

// setupLogging configures the shared logger for the profiler CLI. opts.level
// sets the verbosity threshold. Output destination priority:
//   - opts.file == "stdout": write to standard output
//   - opts.file is a path: rotate through filerotate, capped at opts.size MB
//   - opts.verbose: write to stdout
//   - otherwise: silenced (io.Discard)
func setupLogging(opts loggingOptions) {
	log.SetFormatter(&prefixFormatter{
		prefix: profilerLogPrefix,
		formatter: &logrus.TextFormatter{
			DisableColors:   true,
			ForceQuote:      true,
			FullTimestamp:   true,
			TimestampFormat: rfc3339NanoFixed,
			DisableSorting:  false,
		},
	})

	if opts.level != "" {
		switch opts.level {
		case "trace", "debug", "info", "warn", "error":
			log.SetLevel(opts.level)
		default:
			fmt.Fprintf(os.Stderr, "invalid log-level %q; using info (allowed: trace|debug|info|warn|error)\n", opts.level)
			log.SetLevel("info")
		}
	}

	size := opts.size
	if size <= 0 {
		size = defaultLogSizeMB
	}

	switch {
	case opts.file == "stdout":
		log.SetOutput(os.Stdout)
	case opts.file != "":
		log.SetOutput(filerotate.NewFileRotator(opts.file, 1, size))
	case opts.verbose:
		log.SetOutput(os.Stdout)
	default:
		log.SetOutput(io.Discard)
	}
}

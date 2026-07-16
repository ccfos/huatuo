// Copyright 2025, 2026 The HuaTuo Authors
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
	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/filerotate"
	"huatuo-bamai/internal/log"
)

const (
	rfc3339NanoFixed  = "2006-01-02T15:04:05.000000000Z07:00"
	profilerLogPrefix = "profiler"
	loggingCloserKey  = "profiler-logging-closer"
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

// setupLogging configures the logger for the profiler CLI and returns the file
// closer so buffered resources are released before the process exits.
// --verbose unconditionally overrides log-level to debug and log-file to
// stdout so that a single flag enables full diagnostic output regardless
// of any explicit --log-level or --log-file values.
func setupLogging(opts loggingOptions) (io.Closer, error) {
	if opts.verbose {
		opts.level = "debug"
		opts.file = "stdout"
	}

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

	switch opts.level {
	case "trace", "debug", "info", "warn", "error":
		log.SetLevel(opts.level)
	default:
		return nil, fmt.Errorf("invalid --log-level %q; allowed: trace, debug, info, warn, error", opts.level)
	}

	if opts.size < 0 {
		return nil, fmt.Errorf("--log-size must be at least 0 MB")
	}

	if opts.file == "stdout" {
		log.SetOutput(os.Stdout)
		return nil, nil
	}

	if opts.file == "" {
		return nil, fmt.Errorf("--log-file must be a file path or stdout")
	}

	if opts.size == 0 {
		file, err := os.OpenFile(opts.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open log file %q: %w", opts.file, err)
		}
		log.SetOutput(file)
		return file, nil
	}

	// Lumberjack opens files lazily, so preflight the path to fail during CLI
	// startup instead of dropping the first log entry.
	file, err := os.OpenFile(opts.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", opts.file, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close log file %q after validation: %w", opts.file, err)
	}

	rotator := filerotate.NewFileRotator(opts.file, 1, opts.size)
	log.SetOutput(rotator)
	return rotator, nil
}

func closeLogging(ctx *cli.Context) error {
	if ctx.App.Metadata == nil {
		return nil
	}
	closer, ok := ctx.App.Metadata[loggingCloserKey].(io.Closer)
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		return fmt.Errorf("close log file: %w", err)
	}
	delete(ctx.App.Metadata, loggingCloserKey)
	return nil
}

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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/pkg/tracing"

	"github.com/urfave/cli/v2"
)

const (
	flagConfig         = "config"
	flagConfigDir      = "config-dir"
	flagBPFObjDir      = "bpf-dir"
	flagToolBinDir     = "tools-bin-dir"
	flagRegion         = "region"
	flagDisableKubelet = "disable-kubelet"
	flagDisableStorage = "disable-storage"
	flagDisableCgroup  = "disable-cgroup"
	flagDisableTracing = "disable-tracing"
	flagLogDebug       = "log-debug"
	flagDryRun         = "dry-run"
	flagProcfsPrefix   = "procfs-prefix"
)

type buildInfo struct {
	Version   string
	GitCommit string
	BuildTime string
}

// Options holds all CLI-derived configuration. Populated by FromContext
// during app.Before so downstream code reads only from Options and stays
// decoupled from the urfave/cli framework.
type Options struct {
	ConfigFile     string
	ConfigDir      string
	BPFObjDir      string
	ToolBinDir     string
	Region         string
	DisableKubelet bool
	DisableStorage bool
	DisableCgroup  bool
	DisableTracing []string
	LogDebug       bool
	DryRun         bool
	ProcfsPrefix   string
}

func buildCommand(info buildInfo) *cli.App {
	opts := &Options{}
	app := cli.NewApp()
	app.Name = appName
	app.Usage = appUsage
	app.Version = formatVersion(info)
	opts.AddFlags(app)

	app.Before = func(ctx *cli.Context) error {
		if err := opts.FromContext(ctx); err != nil {
			return err
		}
		return configureRuntime(opts)
	}

	app.Action = func(ctx *cli.Context) error {
		if ctx.NArg() > 0 {
			return fmt.Errorf("invalid param %v", ctx.Args())
		}
		return mainAction(opts)
	}

	return app
}

func formatVersion(info buildInfo) string {
	return strings.Join([]string{
		"",
		fmt.Sprintf("   app_version: %s", info.Version),
		fmt.Sprintf("   go_version: %s", runtime.Version()),
		fmt.Sprintf("   git_commit: %s", info.GitCommit),
		fmt.Sprintf("   build_time: %s", info.BuildTime),
	}, "\n")
}

// AddFlags registers every CLI flag onto app.Flags.
func (o *Options) AddFlags(app *cli.App) {
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  flagConfig,
			Value: "huatuo-bamai.conf",
			Usage: "huatuo-bamai config file",
		},
		&cli.StringFlag{
			Name:  flagConfigDir,
			Value: "conf",
			Usage: "huatuo config dir",
		},
		&cli.StringFlag{
			Name:  flagBPFObjDir,
			Value: "bpf",
			Usage: "bpf obj dir",
		},
		&cli.StringFlag{
			Name:  flagToolBinDir,
			Value: "bin",
			Usage: "tools bin dir",
		},
		&cli.StringFlag{
			Name:     flagRegion,
			Required: true,
			Usage:    "the host and containers are in this region",
		},
		&cli.BoolFlag{
			Name:  flagDisableKubelet,
			Value: false,
			Usage: "disable kubelet(testing only). Not recommended for production use.",
		},
		&cli.BoolFlag{
			Name:  flagDisableStorage,
			Value: false,
			Usage: "disable storage backends(testing only). Not recommended for production use.",
		},
		&cli.BoolFlag{
			Name:  flagDisableCgroup,
			Value: false,
			Usage: "disable self cgroup resource limit",
		},
		&cli.StringSliceFlag{
			Name:  flagDisableTracing,
			Usage: "disable tracing. This is related to BlackList in config, and complement each other",
		},
		&cli.BoolFlag{
			Name:  flagLogDebug,
			Usage: "enable debug output for logging",
		},
		&cli.BoolFlag{
			Name:  flagDryRun,
			Usage: "for loading tests, exit gracefully",
		},
		&cli.StringFlag{
			Name:  flagProcfsPrefix,
			Usage: "procfs prefix for default mountpoint e.g. /proc /sys and /dev",
		},
	}
}

// FromContext copies parsed flag values from urfave/cli into Options.
func (o *Options) FromContext(ctx *cli.Context) error {
	o.ConfigFile = ctx.String(flagConfig)
	o.Region = ctx.String(flagRegion)
	o.DisableKubelet = ctx.Bool(flagDisableKubelet)
	o.DisableStorage = ctx.Bool(flagDisableStorage)
	o.DisableCgroup = ctx.Bool(flagDisableCgroup)
	o.DisableTracing = ctx.StringSlice(flagDisableTracing)
	o.LogDebug = ctx.Bool(flagLogDebug)
	o.DryRun = ctx.Bool(flagDryRun)
	o.ProcfsPrefix = ctx.String(flagProcfsPrefix)

	var err error
	if o.ConfigDir, err = resolveOptionDir(ctx, flagConfigDir); err != nil {
		return err
	}
	if o.BPFObjDir, err = resolveOptionDir(ctx, flagBPFObjDir); err != nil {
		return err
	}
	if o.ToolBinDir, err = resolveOptionDir(ctx, flagToolBinDir); err != nil {
		return err
	}

	return nil
}

// resolveOptionDir returns an absolute directory path for a path-like flag.
// Absolute values and explicitly-set relative values are returned as-is;
// unset defaults are anchored to the binary's parent dir to preserve the
// original layout-relative resolution.
func resolveOptionDir(ctx *cli.Context, name string) (string, error) {
	dir := ctx.String(name)
	if filepath.IsAbs(dir) {
		return dir, nil
	}

	if ctx.IsSet(name) {
		return dir, nil
	}

	runningDir, err := executil.RunningDir()
	if err != nil {
		return "", fmt.Errorf("resolve %s dir: %w", name, err)
	}

	return filepath.Join(runningDir, "../", dir), nil
}

// configureRuntime applies process-global side effects derived from Options:
// config file load, log level/file, tracer blacklist merge, procfs prefix.
// Runs once from app.Before so subsequent code can read config.Get() freely.
func configureRuntime(opts *Options) error {
	bpf.DefaultBpfObjDir = opts.BPFObjDir
	tracing.TaskBinDir = opts.ToolBinDir

	if err := config.Load(filepath.Join(opts.ConfigDir, opts.ConfigFile)); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Log level: config file wins; --log-debug only applies when the config
	// file leaves the level unset.
	switch {
	case config.Get().Log.Level != "":
		log.SetLevel(config.Get().Log.Level)
		log.Infof("log level set to %q from config file", log.GetLevel())
	case opts.LogDebug:
		log.SetLevel("Debug")
		log.Infof("log level set to %q from --log-debug", log.GetLevel())
	}

	if logFile := config.Get().Log.File; logFile != "" {
		// File handle is kept open for the process lifetime as the log sink.
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			log.SetOutput(os.Stdout)
			log.Infof("open log file %q failed, falling back to stdout: %v", logFile, err)
		} else {
			log.SetOutput(file)
		}
	}

	if len(opts.DisableTracing) > 0 {
		bl := config.Get().BlackList
		merged := make([]string, 0, len(bl)+len(opts.DisableTracing))
		merged = append(merged, bl...)
		merged = append(merged, opts.DisableTracing...)
		config.Set("BlackList", merged)
		log.Infof("merged tracer blacklist from CLI: %v", config.Get().BlackList)
	}

	if opts.ProcfsPrefix != "" {
		procfs.RootPrefix(opts.ProcfsPrefix)
	}

	log.Debugf("option %s: %s, %s: %s, %s: %s",
		flagBPFObjDir, bpf.DefaultBpfObjDir,
		flagToolBinDir, tracing.TaskBinDir,
		flagConfigDir, opts.ConfigDir)

	return nil
}

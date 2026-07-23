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
	"path/filepath"
	"strings"

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/internal/version"

	"github.com/urfave/cli/v2"
)

const (
	cliFlagConfig        = "config"
	cliFlagConfigDir     = "config-dir"
	cliFlagEnablePProf   = "enable-pprof"
	cliFlagDisableCgroup = "disable-cgroup"
)

// Options holds CLI-derived configuration independently of urfave/cli.
type Options struct {
	ConfigFile    string
	ConfigDir     string
	EnablePProf   bool
	DisableCgroup bool
	VersionInfo   version.Info
	Config        *config.Config
}

func buildCommand(seed version.Seed) *cli.App {
	opts := &Options{}
	app := cli.NewApp()
	app.Name = appName
	app.Usage = appUsage
	opts.AddFlags(app)
	opts.VersionInfo = version.Wire(app, seed)

	app.Before = func(ctx *cli.Context) error {
		if err := opts.FromContext(ctx); err != nil {
			return err
		}
		return configureRuntime(opts)
	}

	app.Action = func(ctx *cli.Context) error {
		if ctx.NArg() > 0 {
			return fmt.Errorf("unexpected positional arguments: %v", ctx.Args().Slice())
		}
		return mainAction(opts)
	}

	return app
}

// AddFlags registers every CLI flag onto app.Flags.
func (o *Options) AddFlags(app *cli.App) {
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  cliFlagConfig,
			Value: "huatuo-apiserver.conf",
			Usage: "huatuo-apiserver config file",
		},
		&cli.StringFlag{
			Name:  cliFlagConfigDir,
			Value: "conf",
			Usage: "huatuo config dir",
		},
		&cli.BoolFlag{
			Name:  cliFlagEnablePProf,
			Usage: "package pprof serves via its HTTP server runtime profiling data, default(false)",
		},
		&cli.BoolFlag{
			Name:  cliFlagDisableCgroup,
			Usage: "disable self cgroup resource limit",
		},
	}
}

// FromContext copies parsed flags into Options.
func (o *Options) FromContext(ctx *cli.Context) error {
	o.ConfigFile = ctx.String(cliFlagConfig)
	o.EnablePProf = ctx.Bool(cliFlagEnablePProf)
	o.DisableCgroup = ctx.Bool(cliFlagDisableCgroup)

	var err error
	if ctx.IsSet(cliFlagConfigDir) {
		o.ConfigDir = ctx.String(cliFlagConfigDir)
		return nil
	}
	o.ConfigDir, err = resolveOptionDir(ctx.String(cliFlagConfigDir))
	return err
}

func resolveOptionDir(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}

	runningDir, err := executil.RunningDir()
	if err != nil {
		return "", fmt.Errorf("resolve %s dir: %w", cliFlagConfigDir, err)
	}

	return filepath.Join(runningDir, "../", dir), nil
}

func configureRuntime(opts *Options) error {
	cfg, err := config.LoadFile(filepath.Join(opts.ConfigDir, opts.ConfigFile))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	opts.Config = cfg
	if dsn := cfg.TaskConfig.JobStoreDSN; !filepath.IsAbs(dsn) && !strings.HasPrefix(dsn, "file:") {
		cfg.TaskConfig.JobStoreDSN = filepath.Join(opts.ConfigDir, dsn)
	}

	if level := cfg.LogLevel; level != "" {
		log.SetLevel(level)
		log.WithField("level", log.GetLevel()).Info("configured log level")
	}

	return nil
}

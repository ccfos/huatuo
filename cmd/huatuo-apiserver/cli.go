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
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/config"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/version"

	"github.com/urfave/cli/v2"
)

const (
	cliFlagConfig       = "config"
	cliFlagConfigDir    = "config-dir"
	cliFlagEnablePProf  = "enable-pprof"
	cliFlagEnableCgroup = "enable-cgroup"
	cliFlagLogDebug     = "log-debug"
)

// Options holds CLI-derived configuration independently of urfave/cli.
type Options struct {
	ConfigFile   string
	ConfigDir    string
	EnablePProf  bool
	EnableCgroup bool
	LogDebug     bool
	VersionInfo  version.Info
	Config       *config.Config
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
			Name:  cliFlagEnableCgroup,
			Usage: "enable self cgroup resource limit",
		},
		&cli.BoolFlag{
			Name:  cliFlagLogDebug,
			Usage: "force debug-level logging; overrides Log.Level from config file",
		},
	}
}

// FromContext copies parsed flags into Options.
func (o *Options) FromContext(ctx *cli.Context) error {
	o.ConfigFile = ctx.String(cliFlagConfig)
	o.EnablePProf = ctx.Bool(cliFlagEnablePProf)
	o.EnableCgroup = ctx.Bool(cliFlagEnableCgroup)
	o.LogDebug = ctx.Bool(cliFlagLogDebug)

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

	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve %s dir: %w", cliFlagConfigDir, err)
	}

	return filepath.Join(filepath.Dir(executable), "../", dir), nil
}

func configureRuntime(opts *Options) error {
	cfg, err := config.LoadFile(filepath.Join(opts.ConfigDir, opts.ConfigFile))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	opts.Config = cfg
	cfg.Jobs.StoreDSN, err = resolveJobStoreDSN(opts.ConfigDir, cfg.Jobs.StoreDSN)
	if err != nil {
		return err
	}

	switch {
	case opts.LogDebug:
		log.SetLevel("Debug")
		log.WithField("level", log.GetLevel()).Info("configured log level from --log-debug")
	case cfg.Log.Level != "":
		level := cfg.Log.Level
		log.SetLevel(level)
		log.WithField("level", log.GetLevel()).Info("configured log level")
	}

	return nil
}

func resolveJobStoreDSN(configDir, dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "file:") {
		if filepath.IsAbs(dsn) {
			return dsn, nil
		}
		return filepath.Join(configDir, dsn), nil
	}

	uri, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse job store URI: %w", err)
	}
	if uri.Opaque == "" || uri.Opaque == ":memory:" || uri.Query().Get("mode") == "memory" {
		return dsn, nil
	}
	path, err := url.PathUnescape(uri.Opaque)
	if err != nil {
		return "", fmt.Errorf("decode job store URI path: %w", err)
	}
	if filepath.IsAbs(filepath.FromSlash(path)) {
		return dsn, nil
	}
	absolutePath, err := filepath.Abs(filepath.Join(configDir, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("resolve job store URI path: %w", err)
	}
	uri.Opaque = ""
	uri.Path = filepath.ToSlash(absolutePath)
	if volume := filepath.VolumeName(absolutePath); len(volume) == 2 && volume[1] == ':' {
		uri.Path = "/" + uri.Path
	}
	return uri.String(), nil
}

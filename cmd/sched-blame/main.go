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
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/core/autotracing"
	"github.com/ccfos/huatuo/internal/bpf"
	internalconfig "github.com/ccfos/huatuo/internal/config"
	serverpprof "github.com/ccfos/huatuo/internal/httpserver/pprof"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/version"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	schedBlameToolName     = "sched-blame"
	flagConfig             = "config"
	flagBPFDir             = "bpf-dir"
	flagOutputFile         = "output-file"
	flagHighlightContainer = "highlight-container"
	flagRatioDebugFile     = "ratio-debug-file"
	flagLogDebug           = "log-debug"
	flagPprofAddr          = "pprof-addr"
)

// Populated by the shared Makefile's linker flags.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

func newApp() *cli.App {
	app := cli.NewApp()
	app.Name = schedBlameToolName
	app.Usage = "Trace cgroup CPU scheduling contention and record anomalies to a file"
	app.Action = mainAction
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  flagConfig,
			Value: filepath.Join(filepath.Dir(internalconfig.CoreBinDir), "conf", "huatuo-bamai.conf"),
			Usage: "path to the Huatuo TOML configuration",
		},
		&cli.StringFlag{
			Name:  flagBPFDir,
			Value: internalconfig.CoreBpfDir,
			Usage: "directory containing sched_blame.o and container-discovery BPF objects",
		},
		&cli.StringFlag{
			Name:     flagOutputFile,
			Required: true,
			Usage:    "append human-readable anomaly events to this file",
		},
		&cli.StringFlag{
			Name:  flagHighlightContainer,
			Usage: "override HighlightContainer with an ID, unique prefix, or hostname",
		},
		&cli.StringFlag{
			Name:  flagRatioDebugFile,
			Usage: "override ExternalRatioDebugFile with a ratio CSV path",
		},
		&cli.BoolFlag{
			Name:  flagLogDebug,
			Usage: "enable debug logging and highlighted-container diagnostics",
		},
		&cli.StringFlag{
			Name:  flagPprofAddr,
			Usage: "serve Go runtime profiles at this address, for example 127.0.0.1:6060",
		},
	}
	version.Wire(app, version.Seed{
		Name:      schedBlameToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})
	return app
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", schedBlameToolName, err)
		os.Exit(1)
	}
}

func mainAction(cliCtx *cli.Context) (returnErr error) {
	if cliCtx.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v; pass paths with --config and --bpf-dir", cliCtx.Args().Slice())
	}
	config, err := loadStandaloneConfig(cliCtx)
	if err != nil {
		return err
	}
	autotracing.Set(&config.AutoTracing)
	log.SetLevel(config.Log.Level)
	if cliCtx.Bool(flagLogDebug) {
		log.SetLevel("Debug")
	}
	if config.Log.File != "" {
		file, err := os.OpenFile(config.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			log.Infof("failed to open log file %q, using stdout: %v", config.Log.File, err)
		} else {
			log.SetOutput(file)
			defer func() {
				log.SetOutput(os.Stdout)
				returnErr = errors.Join(returnErr, file.Close())
			}()
		}
	}

	ctx, stop := signal.NotifyContext(cliCtx.Context,
		syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if address := cliCtx.String(flagPprofAddr); address != "" {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("listen for pprof on %s: %w", address, err)
		}
		server, err := serverpprof.Start(ctx, listener)
		if err != nil {
			_ = listener.Close()
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, server.Close()) }()
		log.Infof("sched-blame: pprof listening on %s", listener.Addr())
	}

	output, err := newSchedBlameEventFile(cliCtx.String(flagOutputFile))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, output.Close()) }()
	bpf.DefaultObjDir = cliCtx.String(flagBPFDir)
	if err := bpf.Init(&bpf.Option{}); err != nil {
		return fmt.Errorf("initialize BPF: %w", err)
	}
	defer bpf.Shutdown()
	if err := pod.InitManager(&pod.ManagerCtx{
		PodReadOnlyPort:   config.Pod.KubeletReadOnlyPort,
		PodAuthorizedPort: config.Pod.KubeletAuthorizedPort,
		PodClientCertPath: config.Pod.KubeletClientCertPath,
		DockerAPIVersion:  config.Pod.DockerAPIVersion,
	}); err != nil {
		return fmt.Errorf("initialize container discovery: %w", err)
	}
	defer pod.ReleaseManager()

	log.Infof("sched-blame standalone: config=%s bpf_dir=%s output=%s",
		cliCtx.String(flagConfig), bpf.DefaultObjDir, cliCtx.String(flagOutputFile))
	err = autotracing.RunSchedBlame(ctx, output.Write)
	if ctx.Err() != nil && (errors.Is(err, types.ErrExitByCancelCtx) || errors.Is(err, ctx.Err())) {
		return nil
	}
	return err
}

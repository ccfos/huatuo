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

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/cmd/huatuo-bamai/handlers"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
)

func (d *Daemon) setupBPF() error {
	if err := bpf.NewManager(&bpf.Option{}); err != nil {
		return fmt.Errorf("failed to init bpf manager: %w", err)
	}
	d.bpfReady = true

	return nil
}

func (d *Daemon) startToolstream() error {
	srv, err := toolstream.NewServerDefault()
	if err != nil {
		return fmt.Errorf("toolstream: %w", err)
	}

	if err := srv.Start(); err != nil {
		return fmt.Errorf("toolstream: start: %w", err)
	}
	d.tools = srv

	return nil
}

func (d *Daemon) startTracing() error {
	mgr, err := tracing.NewManager(config.Get().BlackList)
	if err != nil {
		return err
	}

	if err := mgr.Start(); err != nil {
		return err
	}
	d.tracing = mgr

	return nil
}

func (d *Daemon) startHandlers() error {
	handlers.Start(config.Get().APIServer.TCPAddr, d.tracing, d.metrics)
	return nil
}

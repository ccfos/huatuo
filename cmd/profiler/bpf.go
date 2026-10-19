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
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
)

// initBpfManager prepares shared BPF resources for native profilers. The
// returned cleanup must run on exit so map FDs and pinned objects are released.
func initBpfManager(cfg *commandConfig) (func(), error) {
	option := &bpf.Option{
		KeepaliveTimeout: int(cfg.Provider.Duration / time.Second),
		Mock:             cfg.BPFMock,
	}
	if option.Mock {
		option.MockObjects = []string{"native_" + string(cfg.Provider.Mode) + ".o"}
	}
	if err := bpf.Init(option); err != nil {
		return nil, fmt.Errorf("init bpf: %w", err)
	}

	return bpf.Shutdown, nil
}

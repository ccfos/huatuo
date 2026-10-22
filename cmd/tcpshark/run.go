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
	"fmt"
	"time"

	"github.com/ccfos/huatuo/cmd/tcpshark/retransmit"
	"github.com/ccfos/huatuo/internal/bpf"
)

type runOptions struct {
	mode            string
	durationSeconds int
	retransmit      retransmit.RunConfig
}

func mainAction(ctx context.Context, options *runOptions) error {
	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: options.durationSeconds}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	if options.durationSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.durationSeconds)*time.Second)
		defer cancel()
	}

	switch options.mode {
	case modeRetransmit:
		return retransmit.Run(ctx, &options.retransmit)
	default:
		return fmt.Errorf("unsupported mode %q", options.mode)
	}
}

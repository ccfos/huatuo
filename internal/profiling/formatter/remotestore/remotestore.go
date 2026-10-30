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

package remotestore

import (
	"context"
	"fmt"
	"os"
	"time"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

type formatter struct {
	client *toolstream.Client
}

func (*formatter) Name() output.OutputFormat { return output.FormatRemoteStore }

func (f *formatter) Add(ctx context.Context, window *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return f.client.Send(ctx, window, toolstream.SendOptions{})
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return f.client.Send(ctx, nil, toolstream.SendOptions{
		End:    true,
		Commit: true,
	})
}

// NewFormatter connects remote output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	if err := output.ValidateDestination(cfg.Destination); err != nil {
		return nil, err
	}

	path := cfg.Destination
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect remote profiling destination %q: %w", path, err)
	}

	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("remote profiling destination %q must be a Unix socket", path)
	}

	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: profiling.StoreServiceName,
		ToolName:    cfg.ToolName,
		TaskID:      cfg.TaskID,
		Version:     "3",
		SendTimeout: 30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("connect remote profiling destination %q: %w", path, err)
	}

	return &formatter{client: client}, nil
}

func (f *formatter) Close() error { return f.client.Close() }

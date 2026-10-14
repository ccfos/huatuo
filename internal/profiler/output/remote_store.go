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

package output

import (
	"context"
	"fmt"
	"os"

	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

type remoteStoreFormatter struct {
	toolstreamClient *toolstream.Client
}

func (f *remoteStoreFormatter) Add(ctx context.Context, window *types.ProfilingWindow) error {
	return f.toolstreamClient.Send(ctx, window, toolstream.SendOptions{})
}

func (f *remoteStoreFormatter) Complete(ctx context.Context) error {
	return f.toolstreamClient.Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true})
}

func newRemoteStoreFormatter(path string, task TaskIdentity) (*remoteStoreFormatter, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect remote profiling destination %q: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("remote profiling destination %q must be a Unix socket", path)
	}
	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: path, ToolName: task.ToolName, TaskID: task.TaskID, Version: task.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("connect remote profiling destination %q: %w", path, err)
	}
	return &remoteStoreFormatter{toolstreamClient: client}, nil
}

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

package profiling

import (
	"context"
	"errors"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/profiling"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
)

// Register initializes result storage before exposing the shared tool handlers.
// Stop the server before calling the returned cleanup function.
func Register(
	ctx context.Context,
	server *toolstream.Server,
	cfg *profilingstore.Config,
	region string,
) (func(context.Context) error, error) {
	if server == nil {
		return nil, errors.New("register profiling: server is required")
	}

	store, err := profilingstore.NewFromConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	writer, err := NewDocumentWriter(store, document.New(region))
	if err != nil {
		return nil, errors.Join(err, store.Close(ctx))
	}

	toolstream.Register(server, profiling.StoreServiceName, writer.HandleStream)

	return store.Close, nil
}

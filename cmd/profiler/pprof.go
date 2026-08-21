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
	"net"

	serverpprof "huatuo-bamai/internal/server/pprof"
)

const profilerPprofAddress = ":6000"

func startPprofServer(ctx context.Context, address string) (*serverpprof.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for pprof server on %s: %w", address, err)
	}

	server, err := serverpprof.Start(ctx, listener)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	return server, nil
}

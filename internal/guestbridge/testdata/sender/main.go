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
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/vsock"
)

func main() {
	if err := send(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func send() error {
	if len(os.Args) != 5 {
		return fmt.Errorf("usage: sender PORT CONTAINER_ID FOREIGN_ID tracing|profiling")
	}
	port, err := strconv.ParseUint(os.Args[1], 10, 32)
	if err != nil {
		return err
	}
	tool := vsock.TracingTool
	if os.Args[4] == "profiling" {
		tool = vsock.ProfilingTool
	}
	b, err := vsock.New(
		vsock.Options{
			Port:          uint32(port),
			ToolName:      tool,
			Version:       "integration",
			IOTimeout:     3 * time.Second,
			RetryInterval: 100 * time.Millisecond,
		},
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer b.Close(ctx)
	base := map[string]any{
		"tracer_id":          "guest-test",
		"tracer_name":        "cpu",
		"hostname":           "forged",
		"region":             "forged",
		"source_type":        "host",
		"container_id":       os.Args[2],
		"tracer_type":        "event",
		"observed_timestamp": "2026-10-16T01:00:00Z",
		"tracer_data":        map[string]any{"counter": json.Number("18446744073709551615")},
	}
	if tool == vsock.ProfilingTool {
		base["tracer_type"] = "profiling"
		base["started_timestamp"] = "2026-10-16T01:00:00Z"
		delete(base, "tracer_data")
		base["profile_data"] = map[string]any{
			"profile_type": "cpu",
			"profile": map[string]any{
				"time_nanos": json.Number("9007199254740993"),
				"sample":     []any{map[string]any{"value": []any{json.Number("9007199254740993")}}},
			},
		}
	}
	// Send an impersonation attempt first, then the valid observation on the same
	// authenticated connection. The Host must reject the first without misrouting it.
	for _, id := range []string{os.Args[3], os.Args[2]} {
		base["container_id"] = id
		data, err := json.Marshal(base)
		if err != nil {
			return err
		}
		for {
			err = b.Save(ctx, driver.Record{Data: data}, driver.SaveOptions{})
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return err
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return b.Close(ctx)
}

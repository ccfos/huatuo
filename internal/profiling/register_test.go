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
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream"
	profilingapi "github.com/ccfos/huatuo/pkg/profiling"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestCommitRequiresDurableWindows(t *testing.T) {
	for _, tool := range []string{"profiler", "irqtracing"} {
		for _, failWrite := range []bool{false, true} {
			name := tool
			if failWrite {
				name += "/write-failure"
			}

			t.Run(name, func(t *testing.T) {
				writer, backend := newPersistentDocumentWriter(t)
				writeErr := errors.New("storage rejected window")
				if failWrite {
					backend.saveErr = writeErr
				}

				path := filepath.Join(t.TempDir(), "profile.sock")
				server, err := toolstream.NewServer(path)
				if err != nil {
					t.Fatal(err)
				}

				toolstream.Register(server, profilingapi.StoreServiceName, writer.HandleStream)
				if err := server.Start(); err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() {
					if err := server.Close(); err != nil {
						t.Error(err)
					}
				})
				version := "2"
				if tool == "profiler" {
					version = "3"
				}

				client, err := toolstream.NewClient(&toolstream.ClientOptions{
					ServiceName: profilingapi.StoreServiceName,
					SockPath:    path,
					ToolName:    tool,
					Version:     version,
					TaskID:      "request",
				})
				if err != nil {
					t.Fatal(err)
				}

				count := 1
				if tool == "profiler" {
					count = 2
				}

				for i := range count {
					window := validProfilingWindow()
					window.Profile.TimeNanos += int64(i) * int64(time.Second)
					var payload any = window
					if tool == "profiler" {
						payload = &types.AggregationWindow{
							ProfileType: window.ProfileType,
							Aggregated: types.AggregatedProfile{
								TimeNanos: window.Profile.TimeNanos,
								Frames:    []types.ProfileFrame{{Name: "main"}, {Name: "work"}},
								Stacks:    [][]uint32{{0, 1}},
								Samples: []types.AggregatedSample{{
									UserStack: 1,
									Value:     70000000,
								}},
							},
						}
					}

					if err := client.Send(t.Context(), payload, toolstream.SendOptions{}); err != nil {
						t.Fatal(err)
					}
				}

				defer client.Close()
				commitErr := client.Send(t.Context(), nil, toolstream.SendOptions{
					End:    true,
					Commit: true,
				})
				if (commitErr != nil) != failWrite {
					t.Fatalf("Send(commit): %v", commitErr)
				}

				if err := server.Close(); err != nil {
					t.Fatal(err)
				}

				if failWrite {
					if !strings.Contains(commitErr.Error(), writeErr.Error()) || backend.commits != 0 {
						t.Fatalf("error=%v publications=%d", commitErr, backend.commits)
					}

					return
				}

				if backend.syncWrites != count || backend.asyncWrites != 0 || backend.commits != 1 {
					t.Fatalf(
						"sync=%d async=%d publications=%d",
						backend.syncWrites,
						backend.asyncWrites,
						backend.commits,
					)
				}

				var document profilingstore.Document
				if err := json.Unmarshal(backend.lastRecord.Data, &document); err != nil {
					t.Fatal(err)
				}

				if tool == "profiler" &&
					(len(document.ProfileData.Profile.Sample) != 1 || document.ProfileData.Profile.Sample[0].Value[0] != 70000000) {
					t.Fatalf("persisted aggregate: %v", document.ProfileData.Profile)
				}

				if document.TracerName != tool || document.TracerID != "request" {
					t.Fatalf("document = %+v", document)
				}
			})
		}
	}
}

func TestCommitPropagatesPublicationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Start(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	}()
	writer, backend := newPersistentDocumentWriter(t)
	backend.commitErr = errors.New("publication failed")
	toolstream.Register(server, profilingapi.StoreServiceName, writer.HandleStream)
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		ServiceName: profilingapi.StoreServiceName,
		SockPath:    path,
		ToolName:    "profiler",
		Version:     "2",
		TaskID:      "empty",
	})
	if err != nil {
		t.Fatal(err)
	}

	defer client.Close()
	err = client.Send(t.Context(), nil, toolstream.SendOptions{
		End:    true,
		Commit: true,
	})
	if closeErr := server.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	if err == nil || !strings.Contains(err.Error(), backend.commitErr.Error()) || backend.commits != 1 {
		t.Fatalf("error=%v calls=%d", err, backend.commits)
	}
}

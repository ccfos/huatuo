//go:build integration

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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/vsock"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

type documentCaptureBackend struct {
	driver.Backend
	records chan driver.Record
}

func (*documentCaptureBackend) Init(context.Context, string, []driver.Index) error { return nil }
func (b *documentCaptureBackend) Save(_ context.Context, record driver.Record, _ driver.SaveOptions) error {
	b.records <- record
	return nil
}
func (*documentCaptureBackend) Close(context.Context) error { return nil }

func TestDocumentTransportIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	capture := &documentCaptureBackend{records: make(chan driver.Record, 1)}
	backend, err := storage.NewStore[*Document](ctx, "capture", capture, Collection, mapper{})
	if err != nil {
		t.Fatal(err)
	}
	profiles := &Store{store: backend}
	defer profiles.Close(ctx)
	dir := t.TempDir()
	traces, err := tracingstore.NewFromConfig(
		ctx,
		tracingstore.Config{LocalFile: &tracingstore.LocalFileConfig{Path: dir, RotationSizeMiB: 1, MaxRotatedFiles: 1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer traces.Close(ctx)
	events, unsubscribe := traces.Subscribe()
	defer unsubscribe()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	budget, err := transport.NewDecodeBudget(2 << 20)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	handled := make(chan error, 2)
	server, err := transport.ServeWithOptions(listener, func(session *transport.Session, chunk transport.ChunkMsg) {
		if len(chunk.Data) == 0 {
			return
		}
		switch session.ToolName {
		case "tracing-document":
			document, err := tracingstore.DecodeDocument(chunk.Data)
			if err == nil {
				err = traces.Save(document)
			}
			handled <- err
		case "profiling-document":
			document, err := DecodeDocument(chunk.Data)
			if err == nil {
				err = profiles.SaveSync(session.Context, document)
			}
			handled <- err
		default:
			t.Errorf("unexpected route %q", session.ToolName)
		}
	}, transport.ServerOptions{
		MaxMessageSize:   1 << 20,
		DecodeBudget:     budget,
		HandshakeTimeout: time.Second,
		FrameReadTimeout: time.Second,
	})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer server.Close()
	trace := []byte(
		`{"source_type":"kata_guest","tracer_type":"event","tracer_name":"kernel_sched_tick",` +
			`"tracer_id":"guest-event","uploaded_timestamp":"2026-01-01T00:00:00Z",` +
			`"observed_timestamp":"2026-01-01T00:00:01.123456789Z",` +
			`"tracer_data":{"counter":18446744073709551615}}`,
	)
	profile := validDocument()
	profile.SourceType = types.DocumentSourceKataGuest
	profile.ProfileData.Profile.TimeNanos = 9007199254740993
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name string
		data []byte
	}{{"tracing-document", trace}, {"profiling-document", encoded}} {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		client, err := transport.NewClientWithConnContext(
			ctx,
			conn,
			transport.ClientMetadata{ToolName: item.name, Version: "1", TaskID: "task"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.SendChunk(item.data, false); err != nil {
			client.Close()
			t.Fatal(err)
		}
		if err := client.End(); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case err := <-handled:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "kernel_sched_tick"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := tracingstore.DecodeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if document.SourceType != types.DocumentSourceKataGuest ||
		document.TracerData.(map[string]any)["counter"] != json.Number(
			"18446744073709551615",
		) || document.ObservedTimestamp.FormatUTC() != "2026-01-01T00:00:01.123456789Z" || document.UploadedTimestamp.Equal(
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	) {
		t.Errorf("event changed in transport/persistence: %+v", document)
	}
	select {
	case event := <-events:
		if event.SourceType != types.DocumentSourceKataGuest {
			t.Error("subscriber lost origin")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case record := <-capture.records:
		saved, err := DecodeDocument(record.Data)
		if err != nil {
			t.Fatal(err)
		}
		if saved.SourceType != types.DocumentSourceKataGuest ||
			saved.ProfileData.Profile.TimeNanos != 9007199254740993 ||
			saved.StartedTimestamp == nil ||
			saved.UploadedTimestamp.IsZero() {
			t.Errorf("profile changed in transport/persistence: %+v", saved)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestGuestNativeStoresToTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	type receivedDocument struct {
		tool string
		data []byte
	}
	received := make(chan receivedDocument, 2)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := transport.Serve(listener, func(session *transport.Session, chunk transport.ChunkMsg) {
		if len(chunk.Data) > 0 {
			received <- receivedDocument{tool: session.ToolName, data: append([]byte(nil), chunk.Data...)}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	options := &vsock.Options{Version: "integration", DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	traces, err := tracingstore.NewFromConfig(ctx, tracingstore.Config{VSOCK: options})
	if err != nil {
		t.Fatal(err)
	}
	defer traces.Close(ctx)
	profiles, err := NewFromConfig(ctx, Config{VSOCK: options})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close(ctx)
	event, err := tracingstore.DecodeDocument(
		[]byte(
			`{"tracer_id":"guest-event","tracer_name":"cpu","tracer_type":"event",` +
				`"observed_timestamp":"2026-10-16T01:00:00Z","source_type":"kata_guest",` +
				`"tracer_data":{"counter":18446744073709551615}}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	profile := validDocument()
	profile.SourceType = types.DocumentSourceKataGuest
	for _, save := range []func() error{
		func() error { return traces.Save(event) },
		func() error { return profiles.Save(ctx, profile) },
	} {
		for {
			err := save()
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(err)
			case <-time.After(time.Millisecond):
			}
		}
	}
	if err := profiles.SaveSync(ctx, profile); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("remote visibility guarantee=%v", err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case document := <-received:
			seen[document.tool] = true
			switch document.tool {
			case vsock.TracingTool:
				d, err := tracingstore.DecodeDocument(document.data)
				if err != nil {
					t.Fatal(err)
				}
				if d.SourceType != types.DocumentSourceKataGuest ||
					d.TracerData.(map[string]any)["counter"].(json.Number).String() != "18446744073709551615" {
					t.Errorf("event=%+v", d)
				}
			case vsock.ProfilingTool:
				d, err := DecodeDocument(document.data)
				if err != nil {
					t.Fatal(err)
				}
				if d.ProfileData.ProfileType != profile.ProfileData.ProfileType || d.SourceType != types.DocumentSourceKataGuest {
					t.Errorf("profile=%+v", d)
				}
			default:
				t.Errorf("wrong document tool %q", document.tool)
			}
		case <-ctx.Done():
			t.Fatal("Guest native store did not forward documents")
		}
	}
	if !seen[vsock.TracingTool] || !seen[vsock.ProfilingTool] {
		t.Errorf("routed tools=%v", seen)
	}
}

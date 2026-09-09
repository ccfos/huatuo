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

package aggregator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/toolstream/transport"
	"huatuo-bamai/pkg/types"

	capnp "capnproto.org/go/capnp/v3"
)

type toolstreamUploadAggregator struct {
	*uploadTestAggregator
	snapshotReady chan struct{}
}

func (a *toolstreamUploadAggregator) Snapshot(pctx *profctx.ProfilerContext, window profiler.CollectionWindow) (any, error) {
	snapshot, err := a.uploadTestAggregator.Snapshot(pctx, window)
	if err != nil || snapshot == nil {
		return nil, err
	}
	samples := snapshot.(map[string]int64)
	items := make([]*profiler.TreeItem, 0, len(samples))
	for stack, value := range samples {
		items = append(items, &profiler.TreeItem{
			Stack: [][]byte{[]byte(stack)},
			Value: uint64(value),
		})
	}
	data, err := profiler.ParseTree(window.Start, profiler.ProfileTypeCpuSample, items, &profiler.ParseOption{
		Duration: window.End.Sub(window.Start),
	})
	if err != nil {
		return nil, err
	}
	// A large comment forces the real socket write to wait for the receiver.
	data.Profile.Comment = append(data.Profile.Comment, int64(len(data.Profile.StringTable)))
	data.Profile.StringTable = append(data.Profile.StringTable, strings.Repeat("x", 8<<20))
	a.snapshotReady <- struct{}{}
	return data, nil
}

type toolstreamProfileResult struct {
	data *types.ProfilingWindow
	err  error
}

func TestPipelineToolstreamUploadPreservesConcurrentSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: path, ToolName: "profiler", Version: "test", TaskID: "upload-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := readToolstreamUploadHandshake(capnp.NewDecoder(conn)); err != nil {
		t.Fatalf("read handshake: %v", err)
	}

	a := &toolstreamUploadAggregator{
		uploadTestAggregator: newUploadTestAggregator(),
		snapshotReady:        make(chan struct{}, 2),
	}
	p := startUploadConsumer(t, a)
	startedAt := time.Unix(1788912345, 123456789)
	firstEnd := startedAt.Add(10 * time.Second)
	secondEnd := firstEnd.Add(10*time.Second + 123*time.Nanosecond)
	var now atomic.Int64
	now.Store(firstEnd.UnixNano())
	p.now = func() time.Time { return time.Unix(0, now.Load()) }
	p.windowStart = startedAt
	p.pctx.OutputFormat = output.FormatRemote
	p.pctx.ToolstreamClient = client
	p.Enqueue(uploadSample{stack: "existing", value: 3})
	waitUploadSignal(t, a.aggregated)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReceiver := func() { releaseOnce.Do(func() { close(release) }) }
	sending := make(chan struct{})
	received := make(chan toolstreamProfileResult, 2)
	receiverDone := make(chan struct{})
	uploadDone := make(chan struct{})
	uploadErr := make(chan error, 1)
	var secondUploadDone chan struct{}
	// Unblock socket I/O before startUploadConsumer's Stop cleanup can wait.
	t.Cleanup(func() {
		releaseReceiver()
		_ = conn.Close()
		_ = client.Close()
		for _, done := range []<-chan struct{}{receiverDone, uploadDone, secondUploadDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("toolstream upload goroutine did not exit after closing the socket")
			}
		}
	})
	go func() {
		defer close(receiverDone)
		defer conn.Close()
		var prefix [1]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			received <- toolstreamProfileResult{err: err}
			return
		}
		// Seeing the frame prefix proves Send has started, not just Snapshot.
		close(sending)
		<-release
		decoder := capnp.NewDecoder(io.MultiReader(bytes.NewReader(prefix[:]), conn))
		for range 2 {
			data, err := readToolstreamUploadProfile(decoder)
			received <- toolstreamProfileResult{data: data, err: err}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(uploadDone)
		uploadErr <- p.aggregateAndSnapshot(t.Context(), false)
	}()
	waitUploadSignal(t, a.snapshotReady)
	waitUploadSignal(t, sending)
	now.Store(firstEnd.Add(7 * time.Second).UnixNano())
	for _, sample := range []uploadSample{{stack: "existing", value: 5}, {stack: "new", value: 7}} {
		p.Enqueue(sample)
		waitUploadSignal(t, a.aggregated)
	}
	select {
	case <-uploadDone:
		t.Fatalf("upload finished before the paused receiver resumed: %v", <-uploadErr)
	default:
	}
	releaseReceiver()
	waitUploadSignal(t, uploadDone)
	if err := <-uploadErr; err != nil {
		t.Fatalf("first toolstream upload: %v", err)
	}
	assertToolstreamUploadProfile(t, received, map[string]int64{"existing": 3}, profiler.CollectionWindow{
		Start: startedAt, End: firstEnd,
	})

	now.Store(secondEnd.UnixNano())
	secondUploadDone = make(chan struct{})
	go func() {
		defer close(secondUploadDone)
		uploadErr <- p.aggregateAndSnapshot(t.Context(), false)
	}()
	waitUploadSignal(t, secondUploadDone)
	if err := <-uploadErr; err != nil {
		t.Fatalf("second toolstream upload: %v", err)
	}
	assertToolstreamUploadProfile(t, received, map[string]int64{"existing": 5, "new": 7}, profiler.CollectionWindow{
		Start: firstEnd, End: secondEnd,
	})
	waitUploadSignal(t, receiverDone)
	if got := p.overflowCount.Load(); got != 0 {
		t.Fatalf("queue overflow = %d, want 0", got)
	}
}

func readToolstreamUploadHandshake(decoder *capnp.Decoder) error {
	message, err := decoder.Decode()
	if err != nil {
		return err
	}
	defer message.Release()
	root, err := transport.ReadRootMessage(message)
	if err != nil {
		return err
	}
	if root.Which() != transport.Message_Which_connect {
		return fmt.Errorf("first toolstream frame is not a handshake")
	}
	return nil
}

func readToolstreamUploadProfile(decoder *capnp.Decoder) (*types.ProfilingWindow, error) {
	message, err := decoder.Decode()
	if err != nil {
		return nil, err
	}
	defer message.Release()
	root, err := transport.ReadRootMessage(message)
	if err != nil {
		return nil, err
	}
	if root.Which() != transport.Message_Which_chunk {
		return nil, fmt.Errorf("profile toolstream frame is not a chunk")
	}
	chunk, err := root.Chunk()
	if err != nil {
		return nil, err
	}
	payload, err := chunk.Data()
	if err != nil {
		return nil, err
	}
	var event types.ProfilingWindow
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}
	if event.Profile == nil {
		return nil, fmt.Errorf("toolstream event has no profile")
	}
	return &event, nil
}

func assertToolstreamUploadProfile(t *testing.T, received <-chan toolstreamProfileResult, want map[string]int64, window profiler.CollectionWindow) {
	t.Helper()
	var result toolstreamProfileResult
	select {
	case result = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out receiving toolstream profile")
	}
	if result.err != nil {
		t.Fatalf("receive toolstream profile: %v", result.err)
	}
	profile := result.data.Profile
	if profile.TimeNanos != window.Start.UnixNano() || profile.DurationNanos != window.End.Sub(window.Start).Nanoseconds() {
		t.Fatalf("toolstream window = (%d, %d), want (%d, %d)", profile.TimeNanos, profile.DurationNanos,
			window.Start.UnixNano(), window.End.Sub(window.Start).Nanoseconds())
	}
	functions := make(map[uint64]string, len(profile.Function))
	for _, function := range profile.Function {
		if function.Name < 0 || function.Name >= int64(len(profile.StringTable)) {
			t.Fatalf("profile function %d has invalid name index %d", function.Id, function.Name)
		}
		functions[function.Id] = profile.StringTable[function.Name]
	}
	locations := make(map[uint64]string, len(profile.Location))
	for _, location := range profile.Location {
		if len(location.Line) != 1 {
			t.Fatalf("profile location %d has %d lines, want 1", location.Id, len(location.Line))
		}
		name, ok := functions[location.Line[0].FunctionId]
		if !ok {
			t.Fatalf("profile location %d has no function", location.Id)
		}
		locations[location.Id] = name
	}
	got := make(map[string]int64)
	for _, sample := range profile.Sample {
		if len(sample.LocationId) != 1 || len(sample.Value) != 1 {
			t.Fatalf("unexpected profile sample shape: %v", sample)
		}
		name, ok := locations[sample.LocationId[0]]
		if !ok {
			t.Fatalf("profile location %d has no function", sample.LocationId[0])
		}
		got[name] += sample.Value[0]
	}
	if !maps.Equal(got, want) {
		t.Fatalf("toolstream profile = %v, want %v", got, want)
	}
}

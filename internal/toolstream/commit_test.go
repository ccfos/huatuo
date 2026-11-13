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

package toolstream_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/toolstream/transport"

	"github.com/ccfos/huatuo/internal/toolstream"
)

func TestCommitAcknowledgesAllWindowsAndPublication(t *testing.T) {
	for _, failure := range []string{"", "window", "publish", "end"} {
		name := failure
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}
			var windows, commits atomic.Int32
			toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
				if request.Data != nil {
					n := *request.Data
					windows.Add(1)
					if failure == "window" && n == 1 {
						return errors.New("first window failed")
					}
				}
				if request.End && !request.Commit {
					return errors.New("missing commit")
				}
				if !request.Commit || request.PriorErr != nil || request.Err != nil {
					return nil
				}
				if windows.Load() != 2 {
					return errors.New("commit overtook windows")
				}
				commits.Add(1)
				if failure == "publish" {
					return errors.New("publish failed")
				}
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := toolstream.NewClient(&toolstream.ClientOptions{
				SockPath:    path,
				ServiceName: "test",
				Version:     "test",
				TaskID:      "job",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			for _, n := range []int{1, 2} {
				if err := client.Send(t.Context(), n, toolstream.SendOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if failure == "end" {
				if err := client.Send(t.Context(), nil, toolstream.SendOptions{End: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				err = client.Send(ctx, nil, toolstream.SendOptions{
					End:    true,
					Commit: true,
				})
				if (err != nil) != (failure != "") {
					t.Fatalf("Send(commit): %v", err)
				}
				if failure == "window" && !strings.Contains(err.Error(), "first window failed") {
					t.Fatal(err)
				}
			}
			// End has no ACK; closing the server joins any in-flight callback.
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if failure == "window" || failure == "end" {
				want = 0
			}
			if commits.Load() != want {
				t.Fatalf("commits=%d want=%d", commits.Load(), want)
			}
		})
	}
}

func TestCommitCancellationDoesNotClaimRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
		if !request.Commit {
			return nil
		}
		close(entered)
		<-release
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: "test",
		Version:     "test",
		TaskID:      "job",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- client.Send(ctx, nil, toolstream.SendOptions{
			End:    true,
			Commit: true,
		})
	}()
	<-entered
	cancel()
	err = <-result
	close(release)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, toolstream.ErrOutputCanceled) || !strings.Contains(err.Error(), "outcome unconfirmed") {
		t.Fatalf("Send(commit): %v", err)
	}
	// Joining the server waits for the callback even when the ACK was lost.
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentServicesAndDuplicateConnection(t *testing.T) {
	for _, failWindow := range []bool{false, true} {
		name := "success"
		if failWindow {
			name = "failed window"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var profilerCommits, irqCommits atomic.Int32
			toolstream.Register(server, "profiler", func(_ context.Context, request *toolstream.Request[int]) error {
				if request.Data != nil {
					close(entered)
					<-release
					if failWindow {
						return errors.New("window storage failed")
					}
				}
				if request.Commit && request.PriorErr == nil && request.Err == nil {
					profilerCommits.Add(1)
				}
				return nil
			})
			toolstream.Register(server, "irqtracing", func(_ context.Context, request *toolstream.Request[int]) error {
				if !request.Commit {
					return nil
				}
				irqCommits.Add(1)
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			connect := func(service, tool string) *toolstream.Client {
				t.Helper()
				client, err := toolstream.NewClient(&toolstream.ClientOptions{
					SockPath:    path,
					ServiceName: service,
					ToolName:    tool,
					Version:     "test",
					TaskID:      "shared-id",
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Close() })
				return client
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			owner := connect("profiler", "first")
			if err := owner.Send(ctx, 1, toolstream.SendOptions{}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := connect("profiler", "second").Send(ctx, nil, toolstream.SendOptions{
				End:    true,
				Commit: true,
			}); err == nil || !strings.Contains(err.Error(), "already belongs") {
				t.Fatalf("duplicate Send(commit): %v", err)
			}
			// IRQ must finish while the profiler's storage handler remains blocked.
			if err := connect("irqtracing", "").Send(ctx, nil, toolstream.SendOptions{
				End:    true,
				Commit: true,
			}); err != nil {
				t.Fatalf("IRQ Send(commit): %v", err)
			}
			unblock()
			if err := owner.Send(ctx, nil, toolstream.SendOptions{
				End:    true,
				Commit: true,
			}); (err != nil) != failWindow {
				t.Fatalf("owner Send(commit): %v", err)
			}
			want := int32(1)
			if failWindow {
				want = 0
			}
			if profilerCommits.Load() != want || irqCommits.Load() != 1 {
				t.Fatalf("commits profiler=%d irq=%d", profilerCommits.Load(), irqCommits.Load())
			}
		})
	}
}

func TestSendFinalDataAndCommitInOneFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	var windows, commits atomic.Int32
	toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
		if request.Data != nil {
			n := *request.Data
			if n != 42 {
				return errors.New("unexpected final payload")
			}
			windows.Add(1)
		}
		if !request.Commit {
			return nil
		}
		if windows.Load() != 1 {
			return errors.New("commit overtook final payload")
		}
		commits.Add(1)
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: "test",
		Version:     "test",
		TaskID:      "final",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := client.Send(ctx, 42, toolstream.SendOptions{Commit: true}); err == nil {
		t.Fatal("commit without end was accepted")
	}
	if err := client.Send(ctx, 42, toolstream.SendOptions{
		End:    true,
		Commit: true,
	}); err != nil {
		t.Fatal(err)
	}
	if windows.Load() != 1 || commits.Load() != 1 {
		t.Fatalf("windows=%d commits=%d", windows.Load(), commits.Load())
	}
}

func BenchmarkClientSend(b *testing.B) {
	for _, mode := range []string{"background", "cancelable", "send_timeout"} {
		b.Run(mode, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "s")
			server, err := toolstream.NewServer(path)
			if err != nil {
				b.Fatal(err)
			}
			toolstream.Register(server, "benchmark", func(context.Context, *toolstream.Request[string]) error { return nil })
			if err := server.Start(); err != nil {
				b.Fatal(err)
			}
			defer server.Close()
			var timeout time.Duration
			if mode == "send_timeout" {
				timeout = time.Second
			}
			client, err := toolstream.NewClient(&toolstream.ClientOptions{
				SockPath:    path,
				ServiceName: "benchmark",
				Version:     "test",
				SendTimeout: timeout,
			})
			if err != nil {
				b.Fatal(err)
			}
			defer client.Close()
			ctx := context.Background()
			if mode == "cancelable" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := client.Send(ctx, "event payload", toolstream.SendOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newClientPeer(t *testing.T, timeout time.Duration) (*toolstream.Client, net.Conn) {
	t.Helper()
	// Keep Unix socket paths below the kernel limit even in long subtest names.
	dir, err := os.MkdirTemp("", "toolstream-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: "test",
		Version:     "test",
		SendTimeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	message, err := capnp.NewDecoder(peer).Decode()
	if err != nil {
		t.Fatal(err)
	}
	message.Release()
	return client, peer
}

func TestClientOutputErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  error
		other error
	}{
		{
			name:  "canceled",
			cause: context.Canceled,
			want:  toolstream.ErrOutputCanceled,
			other: toolstream.ErrOutputTimeout,
		},
		{
			name:  "deadline cause",
			cause: context.DeadlineExceeded,
			want:  toolstream.ErrOutputTimeout,
			other: toolstream.ErrOutputCanceled,
		},
		{
			name:  "custom cancellation",
			cause: errors.New("operator stopped output"),
			want:  toolstream.ErrOutputCanceled,
			other: toolstream.ErrOutputTimeout,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newClientPeer(t, 0)
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(test.cause)
			err := client.Send(ctx, "event", toolstream.SendOptions{})
			if !errors.Is(err, test.want) || errors.Is(err, test.other) || !errors.Is(err, test.cause) {
				t.Fatalf("Send = %v, want only %v and cause %v", err, test.want, test.cause)
			}
		})
	}

	client, _ := newClientPeer(t, 0)
	cause := errors.New("report budget exhausted")
	ctx, cancel := context.WithDeadlineCause(t.Context(), time.Now().Add(-time.Second), cause)
	defer cancel()
	err := client.Send(ctx, "event", toolstream.SendOptions{})
	if !errors.Is(err, toolstream.ErrOutputTimeout) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cause) || errors.Is(err, toolstream.ErrOutputCanceled) {
		t.Fatalf("Send after deadline = %v", err)
	}
}

func TestClientCancellationInterruptsBlockedWrite(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			client, peer := newClientPeer(t, 0)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			done := make(chan error, 1)
			go func() { done <- client.Send(ctx, strings.Repeat("x", 1<<20), toolstream.SendOptions{}) }()
			var first [1]byte
			if _, err := io.ReadFull(peer, first[:]); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				t.Fatalf("large send completed without a reader: %v", err)
			default:
			}
			cancel(cause)
			select {
			case err := <-done:
				want, other := toolstream.ErrOutputCanceled, toolstream.ErrOutputTimeout
				if errors.Is(cause, context.DeadlineExceeded) {
					want, other = other, want
				}
				if !errors.Is(err, want) || errors.Is(err, other) || !errors.Is(err, cause) {
					t.Fatalf("Send = %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				_ = client.Close()
				<-done
				t.Fatal("cancellation did not unblock send")
			}
			for range 2 {
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type cancelingEvent struct {
	cancel context.CancelFunc
	err    error
}

func (e cancelingEvent) MarshalJSON() ([]byte, error) {
	e.cancel()
	return []byte(`"event"`), e.err
}

func TestClientEncodingCancellationPreservesFailure(t *testing.T) {
	client, _ := newClientPeer(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := errors.New("invalid encoded event")
	err := client.Send(ctx, cancelingEvent{
		cancel: cancel,
		err:    failure,
	}, toolstream.SendOptions{})
	if !errors.Is(err, failure) || errors.Is(err, toolstream.ErrOutputCanceled) || errors.Is(err, toolstream.ErrOutputTimeout) {
		t.Fatalf("Send = %v, want independent encoding failure", err)
	}

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	err = client.Send(ctx, cancelingEvent{cancel: cancel}, toolstream.SendOptions{})
	if !errors.Is(err, toolstream.ErrOutputCanceled) {
		t.Fatalf("cancellation during encoding = %v", err)
	}
	// Pre-send cancellation does not close a connection that has not sent data.
	if err := client.Send(t.Context(), "next event", toolstream.SendOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientCompletedSendDetachesCancellation(t *testing.T) {
	client, peer := newClientPeer(t, 0)
	for range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		err := client.Send(ctx, "event", toolstream.SendOptions{})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		message, err := capnp.NewDecoder(peer).Decode()
		if err != nil {
			t.Fatal(err)
		}
		message.Release()
	}
}

func TestClientPeerFailureIsNotCancellation(t *testing.T) {
	client, peer := newClientPeer(t, 0)
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	err := client.Send(t.Context(), "event", toolstream.SendOptions{})
	if err == nil || errors.Is(err, toolstream.ErrOutputCanceled) || errors.Is(err, toolstream.ErrOutputTimeout) {
		t.Fatalf("Send = %v, want peer failure", err)
	}
}

func TestClientCommitDeadlinePreservesUnconfirmedOutcome(t *testing.T) {
	client, peer := newClientPeer(t, 0)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	done := make(chan error, 1)
	go func() {
		done <- client.Send(ctx, nil, toolstream.SendOptions{
			End:    true,
			Commit: true,
		})
	}()
	message, err := capnp.NewDecoder(peer).Decode()
	if err != nil {
		t.Fatal(err)
	}
	message.Release()
	cancel(context.DeadlineExceeded)
	select {
	case err := <-done:
		if !errors.Is(err, toolstream.ErrOutputTimeout) || !strings.Contains(err.Error(), "outcome unconfirmed") {
			t.Fatalf("Send(commit) = %v", err)
		}
	case <-time.After(time.Second):
		_ = client.Close()
		<-done
		t.Fatal("deadline did not unblock commit acknowledgement")
	}
}

func TestClientRemoteFailureKeepsItsCause(t *testing.T) {
	client, peer := newClientPeer(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Send(ctx, nil, toolstream.SendOptions{
			End:    true,
			Commit: true,
		})
	}()
	request, err := capnp.NewDecoder(peer).Decode()
	if err != nil {
		t.Fatal(err)
	}
	request.Release()
	message, segment, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer message.Release()
	root, err := transport.NewRootMessage(segment)
	if err != nil {
		t.Fatal(err)
	}
	result, err := root.NewResult()
	if err != nil {
		t.Fatal(err)
	}
	if err := result.SetError("storage rejected commit"); err != nil {
		t.Fatal(err)
	}
	if err := capnp.NewEncoder(peer).Encode(message); err != nil {
		t.Fatal(err)
	}
	err = <-done
	cancel()
	if err == nil || !strings.Contains(err.Error(), "storage rejected commit") || errors.Is(err, toolstream.ErrOutputCanceled) || errors.Is(err, toolstream.ErrOutputTimeout) {
		t.Fatalf("Send(commit) = %v", err)
	}
}

func TestClientRejectsNegativeSendTimeout(t *testing.T) {
	_, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    "/does-not-exist",
		ServiceName: "test",
		Version:     "test",
		SendTimeout: -time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "SendTimeout") {
		t.Fatalf("NewClient = %v, want configuration error before dialing", err)
	}
}

func TestClientSendRenewsDeadline(t *testing.T) {
	client, peer := newClientPeer(t, 100*time.Millisecond)
	decoder := capnp.NewDecoder(peer)
	for range 2 {
		// The first send must also work after the constructor's timeout interval.
		time.Sleep(150 * time.Millisecond)
		if err := client.Send(context.Background(), "event", toolstream.SendOptions{}); err != nil {
			t.Fatal(err)
		}
		message, err := decoder.Decode()
		if err != nil {
			t.Fatal(err)
		}
		message.Release()
	}
}

func TestClientSendTimeoutLeavesCloseToCaller(t *testing.T) {
	for _, cancelSend := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelSend), func(t *testing.T) {
			client, peer := newClientPeer(t, 300*time.Millisecond)
			ctx := context.Background()
			cancel := func() {}
			if cancelSend {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- client.Send(ctx, strings.Repeat("x", 1<<20), toolstream.SendOptions{}) }()
			var first [1]byte
			if _, err := io.ReadFull(peer, first[:]); err != nil {
				t.Fatal(err)
			}
			cancel()
			var sendErr error
			select {
			case sendErr = <-done:
			case <-time.After(2 * time.Second):
				_ = client.Close()
				<-done
				t.Fatal("blocked send did not return")
			}
			want, other := toolstream.ErrOutputTimeout, toolstream.ErrOutputCanceled
			if cancelSend {
				want, other = other, want
			}
			if !errors.Is(sendErr, want) || errors.Is(sendErr, other) || !errors.Is(sendErr, os.ErrDeadlineExceeded) {
				t.Fatalf("Send = %v, want only %v and socket deadline", sendErr, want)
			}
			if err := peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			// Drain the partial frame. An open connection times out instead of EOF.
			if _, err := io.Copy(io.Discard, peer); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("peer after interrupted Send = %v, connection was closed early", err)
			}
			if err := client.Send(context.Background(), nil, toolstream.SendOptions{End: true}); !errors.Is(err, sendErr) {
				t.Fatalf("reuse = %v, want original send failure %v", err, sendErr)
			}
			if err := peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if n, err := peer.Read(first[:]); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("failed client wrote another frame: n=%d err=%v", n, err)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := peer.Read(first[:]); !errors.Is(err, io.EOF) {
				t.Fatalf("peer after cleanup = %v, want EOF", err)
			}
		})
	}
}

func TestClientCommitUsesEarlierDeadline(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller_deadline=%v", callerDeadline), func(t *testing.T) {
			timeout := 150 * time.Millisecond
			if callerDeadline {
				timeout = 5 * time.Second
			}
			client, peer := newClientPeer(t, timeout)
			deadlineCause := errors.New("caller send budget exhausted")
			ctx := context.Background()
			cancel := func() {}
			if callerDeadline {
				ctx, cancel = context.WithTimeoutCause(ctx, 150*time.Millisecond, deadlineCause)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- client.Send(ctx, nil, toolstream.SendOptions{
					End:    true,
					Commit: true,
				})
			}()
			message, err := capnp.NewDecoder(peer).Decode()
			if err != nil {
				t.Fatal(err)
			}
			message.Release()
			select {
			case err := <-done:
				if callerDeadline && !errors.Is(err, deadlineCause) {
					t.Fatalf("Commit lost deadline cause: %v", err)
				}
				if !errors.Is(err, toolstream.ErrOutputTimeout) || !strings.Contains(err.Error(), "outcome unconfirmed") {
					t.Fatalf("Commit = %v, want unconfirmed timeout", err)
				}
			case <-time.After(time.Second):
				_ = client.Close()
				<-done
				t.Fatal("Commit did not honor the earlier deadline")
			}
		})
	}
}

func TestClientCommitSharesWriteDeadline(t *testing.T) {
	client, peer := newClientPeer(t, 500*time.Millisecond)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- client.Send(context.Background(), strings.Repeat("x", 1<<20), toolstream.SendOptions{
			End:    true,
			Commit: true,
		})
	}()
	var first [1]byte
	if _, err := io.ReadFull(peer, first[:]); err != nil {
		t.Fatal(err)
	}
	// Consume most of the send budget before allowing the frame write to finish.
	time.Sleep(350 * time.Millisecond)
	message, err := capnp.NewDecoder(io.MultiReader(bytes.NewReader(first[:]), peer)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	message.Release()
	select {
	case err := <-done:
		if !errors.Is(err, toolstream.ErrOutputTimeout) || !strings.Contains(err.Error(), "outcome unconfirmed") {
			t.Fatalf("Commit = %v, want acknowledgement timeout", err)
		}
		if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
			t.Fatalf("Commit renewed the write deadline: %v", elapsed)
		}
	case <-time.After(time.Second):
		_ = client.Close()
		<-done
		t.Fatal("Commit acknowledgement remained blocked")
	}
}

type slowEvent struct {
	delay time.Duration
}

func (e slowEvent) MarshalJSON() ([]byte, error) {
	time.Sleep(e.delay)
	return []byte(`"expired event"`), nil
}

func TestClientEncodingTimeoutKeepsConnectionUsable(t *testing.T) {
	client, peer := newClientPeer(t, 100*time.Millisecond)
	err := client.Send(context.Background(), slowEvent{delay: 150 * time.Millisecond}, toolstream.SendOptions{})
	if !errors.Is(err, toolstream.ErrOutputTimeout) || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Send = %v, want budget exhaustion before I/O", err)
	}
	if err := client.Send(context.Background(), "next event", toolstream.SendOptions{}); err != nil {
		t.Fatalf("connection after encoding timeout: %v", err)
	}
	message, err := capnp.NewDecoder(peer).Decode()
	if err != nil {
		t.Fatal(err)
	}
	defer message.Release()
	root, err := transport.ReadRootMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := root.Chunk()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := chunk.Data()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `"next event"` {
		t.Fatalf("first received payload = %s, expired event reached the socket", payload)
	}
}

func TestClientDefaultSendTimeout(t *testing.T) {
	t.Parallel()
	client, peer := newClientPeer(t, 0)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- client.Send(context.Background(), strings.Repeat("x", 1<<20), toolstream.SendOptions{})
	}()
	var first [1]byte
	if _, err := io.ReadFull(peer, first[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, toolstream.ErrOutputTimeout) || errors.Is(err, toolstream.ErrOutputCanceled) {
			t.Fatalf("default Send = %v, want timeout", err)
		}
		if elapsed := time.Since(started); elapsed < toolstream.DefaultSendTimeout {
			t.Fatalf("default Send timed out too early: %v", elapsed)
		}
	case <-time.After(toolstream.DefaultSendTimeout + time.Second):
		_ = client.Close()
		<-done
		t.Fatal("zero SendTimeout did not select the default")
	}
}

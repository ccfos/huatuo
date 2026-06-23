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

package watch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testDoc struct{}

func TestHub_SubscribeReceivesNotifiedValue(t *testing.T) {
	h := NewHub[*testDoc]()
	ch, cancel := h.Subscribe()
	defer cancel()

	doc := &testDoc{}
	h.Notify(doc)

	select {
	case received := <-ch:
		require.Equal(t, doc, received)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for value")
	}
}

func TestHub_MultipleSubscribersAllReceive(t *testing.T) {
	h := NewHub[*testDoc]()

	ch1, cancel1 := h.Subscribe()
	defer cancel1()
	ch2, cancel2 := h.Subscribe()
	defer cancel2()

	doc := &testDoc{}
	h.Notify(doc)

	for _, ch := range []<-chan *testDoc{ch1, ch2} {
		select {
		case got := <-ch:
			require.Equal(t, doc, got)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for value on subscriber")
		}
	}
}

func TestHub_CancelRemovesSubscriber(t *testing.T) {
	h := NewHub[*testDoc]()
	_, cancel := h.Subscribe()
	cancel()

	h.mu.RLock()
	count := len(h.subs)
	h.mu.RUnlock()

	require.Zero(t, count)
}

func TestHub_SlowSubscriberDoesNotBlock(t *testing.T) {
	h := NewHub[*testDoc]()
	_, cancel := h.Subscribe()
	defer cancel()

	h.mu.RLock()
	internalCh := h.subs[0].ch
	h.mu.RUnlock()

	for i := range defaultBufSize {
		internalCh <- &testDoc{}
		_ = i
	}

	done := make(chan struct{})
	go func() {
		h.Notify(&testDoc{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked on a full subscriber channel")
	}
}

func TestHub_NoSubscribersNotifyIsNoop(t *testing.T) {
	h := NewHub[*testDoc]()
	require.NotPanics(t, func() {
		h.Notify(&testDoc{})
	})
}

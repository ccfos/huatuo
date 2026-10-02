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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/watch"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestStoreSubscriptionsReceiveDocumentsAndCancelIndependently(t *testing.T) {
	store := &Store{hub: watch.NewHub[*Document]()}
	first, cancelFirst := store.Subscribe()
	defer cancelFirst()
	second, cancelSecond := store.Subscribe()
	defer cancelSecond()

	newDocument := func(id string) *Document {
		return &Document{Document: types.Document{
			Hostname:          "node-1",
			TracerID:          id,
			TracerRunType:     types.TracerRunTypeEvent,
			ObservedTimestamp: &timeutil.Timestamp{Time: time.Now()},
		}}
	}
	assertReceived := func(ch <-chan *Document, want *Document) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("received document = %p, want %p", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for document notification")
		}
	}

	firstDocument := newDocument("trace-first")
	if err := store.Save(firstDocument); err != nil {
		t.Fatalf("Store.Save() error = %v", err)
	}
	assertReceived(first, firstDocument)
	assertReceived(second, firstDocument)

	cancelFirst()
	secondDocument := newDocument("trace-second")
	if err := store.Save(secondDocument); err != nil {
		t.Fatalf("Store.Save() error = %v", err)
	}
	assertReceived(second, secondDocument)
	select {
	case got := <-first:
		t.Errorf("canceled subscriber received document = %p", got)
	case <-time.After(50 * time.Millisecond):
	}
}

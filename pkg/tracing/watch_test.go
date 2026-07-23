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

package tracing

import (
	"testing"
	"time"
)

func TestSubscribeReceivesDocumentsAndStopsAfterCancel(t *testing.T) {
	first, cancelFirst := Subscribe()
	defer cancelFirst()
	second, cancelSecond := Subscribe()
	defer cancelSecond()

	firstDocument := &Document{TracerID: "trace-first"}
	NotifySubscribers(firstDocument)
	for _, ch := range []<-chan *Document{first, second} {
		select {
		case got := <-ch:
			if got != firstDocument {
				t.Errorf("received document = %p, want %p", got, firstDocument)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for document notification")
		}
	}

	cancelFirst()
	secondDocument := &Document{TracerID: "trace-second"}
	NotifySubscribers(secondDocument)
	select {
	case got := <-second:
		if got != secondDocument {
			t.Errorf("received document = %p, want %p", got, secondDocument)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active subscriber")
	}
	select {
	case got := <-first:
		t.Errorf("cancelled subscriber received document = %p", got)
	case <-time.After(50 * time.Millisecond):
	}
}

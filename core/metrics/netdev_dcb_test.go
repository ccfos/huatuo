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

package collector

import (
	"strings"
	"testing"
	"unsafe"
)

func TestParseDcbNetlinkMessagesRejectsShortMessage(t *testing.T) {
	// A truncated netlink reply shorter than the dcbMsg header used to panic
	// on m[sizeofDcbmsg:] inside dcbCollector.Update.
	msgs := [][]byte{{0x00, 0x01}, nil, {}}

	_, err := parseDcbNetlinkMessages(msgs)
	if err == nil {
		t.Fatal("expected error for short dcb netlink message, got nil")
	}
	if !strings.Contains(err.Error(), "dcb netlink message too short") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseDcbNetlinkMessagesEmptyList(t *testing.T) {
	pfcs, err := parseDcbNetlinkMessages(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pfcs) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(pfcs))
	}
}

func TestDeserializeIEEEPfcRejectsShortPayload(t *testing.T) {
	size := int(unsafe.Sizeof(ieeePfc{}))
	short := make([]byte, size-1)

	_, err := deserializeIEEEPfc(short)
	if err == nil {
		t.Fatal("expected error for short ieee pfc payload, got nil")
	}
	if !strings.Contains(err.Error(), "ieee pfc attr too short") {
		t.Fatalf("unexpected error: %v", err)
	}
}

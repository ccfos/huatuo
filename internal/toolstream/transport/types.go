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

package transport

//go:generate sh -c "capnp compile -I$(go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3)/std -ogo:. event.capnp"

// ChunkMsg carries a data payload or end-of-stream/error signal.
// Data is backed by the Cap'n Proto message and safe to retain across handler calls.
type ChunkMsg struct {
	Data  []byte
	Flush bool
	End   bool
	Err   string
}

// Session holds per-connection state populated from the Connect frame.
type Session struct {
	ToolName string
	Version  string
	TaskID   string
}

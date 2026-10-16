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

//go:build !linux

package guestbridge

import (
	"context"
	"fmt"
	"net"
)

func (*Bridge) waitConnectionPeer(context.Context, net.Conn, string) (PeerIdentity, error) {
	return PeerIdentity{}, fmt.Errorf("guestbridge: Linux is required")
}
func removeStaleSocket(string) error { return fmt.Errorf("guestbridge: Linux is required") }

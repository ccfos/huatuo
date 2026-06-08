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

package packet

import (
	"fmt"
	"strings"

	"github.com/gopacket/gopacket/layers"
)

func tcpFlags(tcp *layers.TCP) string {
	var b strings.Builder

	b.Grow(32)

	if tcp.SYN {
		b.WriteString("SYN|")
	}

	if tcp.ACK {
		b.WriteString("ACK|")
	}

	if tcp.FIN {
		b.WriteString("FIN|")
	}

	if tcp.RST {
		b.WriteString("RST|")
	}

	if tcp.PSH {
		b.WriteString("PSH|")
	}

	if tcp.URG {
		b.WriteString("URG|")
	}

	if tcp.ECE {
		b.WriteString("ECE|")
	}

	if tcp.CWR {
		b.WriteString("CWR|")
	}

	s := b.String()
	if s != "" {
		return s[:len(s)-1]
	}

	return ""
}

var tcpStateNames = []string{
	"unknown", "ESTABLISHED", "SYN_SENT", "SYN_RECV",
	"FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT", "CLOSE",
	"CLOSE_WAIT", "LAST_ACK", "LISTEN", "CLOSING", "NEW_SYN_RECV",
}

func tcpStateName(state uint8) string {
	if int(state) < len(tcpStateNames) {
		return tcpStateNames[state]
	}

	return fmt.Sprintf("UNKNOWN(%d)", state)
}

// TCPSkState extracts the sk_state string from pi.
// It handles both *TCPInfo (in-process) and map[string]any (JSON-decoded) forms.
func TCPSkState(pi PacketInfo) string {
	switch v := pi.(type) {
	case *TCPInfo:
		return v.SkState
	case map[string]any:
		s, _ := v["tcp_state"].(string)
		return s
	}

	return ""
}

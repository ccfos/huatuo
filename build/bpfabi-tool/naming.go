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

package main

import (
	"strings"
	"unicode"
)

var initialisms = map[string]string{
	"bpf":  "BPF",
	"cpu":  "CPU",
	"css":  "CSS",
	"id":   "ID",
	"io":   "IO",
	"ip":   "IP",
	"lacp": "LACP",
	"n":    "N",
	"ns":   "NS",
	"oom":  "OOM",
	"pid":  "PID",
	"ras":  "RAS",
	"rx":   "RX",
	"skb":  "SKB",
	"tcp":  "TCP",
	"tgid": "TGID",
	"tid":  "TID",
	"tx":   "TX",
}

func goName(cName string) string {
	parts := strings.Split(cName, "_")
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if initialism, ok := initialisms[part]; ok {
			b.WriteString(initialism)
			continue
		}
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}

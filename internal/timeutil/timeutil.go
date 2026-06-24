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

// Package timeutil defines the canonical time format used everywhere
// in the project: storage records, wire protocols, log lines, file
// names. Every serialization boundary must go through FormatUTC; every
// parser must go through Parse. Direct use of time.Format/time.Parse
// in business code is forbidden by lint.
package timeutil

import "time"

// Layout is RFC 3339 with fixed nanosecond precision in UTC.
//
// Why fixed width: lexical string order must equal chronological order
// so that ES keyword fields, log greppers, and file-name sorts behave
// correctly without parsing. time.RFC3339Nano strips trailing zeros and
// produces variable-length strings (e.g. "...56Z" vs "...56.123Z"),
// breaking that invariant across whole-second / fractional-second
// values. Fixed nine-digit width keeps the invariant.
//
// Why nanoseconds (not milliseconds): aligns with internal/log so that
// a log line and a stored event for the same instant share a
// byte-identical timestamp, and matches Go's time.Time internal
// resolution without truncation.
const Layout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatUTC renders t as a Layout string in UTC.
//
// Always use this at every serialization boundary. The .UTC() call
// guarantees every emitted offset is "Z", which is required for the
// lexical-order property to hold across hosts in different timezones.
func FormatUTC(t time.Time) string {
	return t.UTC().Format(Layout)
}

// Parse parses a string written with Layout. It also accepts any
// RFC 3339 variant with 0-9 fractional second digits (whole seconds,
// milliseconds, microseconds, nanoseconds) so that legacy data and
// peers using time.RFC3339Nano remain readable.
//
// The result is always normalized to UTC.
func Parse(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

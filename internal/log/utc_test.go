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

package log

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ccfos/huatuo/internal/timeutil"
)

func TestDefaultFormatterUsesUTC(t *testing.T) {
	for _, offset := range []int{8 * 3600, -7 * 3600, 0} {
		instant := time.Date(2026, time.October, 19, 0, 15, 2, 123456789,
			time.FixedZone("test", offset))
		entry := &logrus.Entry{
			Logger: logger, Time: instant, Level: logrus.InfoLevel,
			Message: "sample collected", Data: logrus.Fields{"tracer_id": "capture"},
		}
		output, err := logger.Formatter.Format(entry)
		if err != nil {
			t.Fatal(err)
		}
		if want := `time="` + timeutil.FormatUTC(instant) + `"`; !strings.Contains(string(output), want) {
			t.Errorf("offset %d: want %s in %s", offset, want, output)
		}
		if entry.Time != instant {
			t.Fatal("formatting changed the caller's entry timestamp")
		}
		if !strings.Contains(string(output), `tracer_id="capture"`) {
			t.Fatalf("structured field missing: %s", output)
		}
	}
}

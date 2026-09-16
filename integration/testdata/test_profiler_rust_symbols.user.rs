// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

static SINK: AtomicU64 = AtomicU64::new(0);

#[inline(never)]
fn consume<T: AsRef<[u8]>>(value: &T, iterations: u64) {
    let bytes = value.as_ref();
    let mut accumulator = 0_u64;

    for index in 0..iterations {
        for byte in bytes {
            accumulator = accumulator.wrapping_add(index.wrapping_mul(u64::from(*byte)));
        }
    }

    SINK.fetch_add(accumulator, Ordering::Relaxed);
}

fn main() {
    let value = [1_u8, 2, 3, 4, 5, 6, 7];
    let deadline = Instant::now() + Duration::from_secs(30);

    while Instant::now() < deadline {
        consume(&value, 200_000);
    }
}

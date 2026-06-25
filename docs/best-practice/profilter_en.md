---
title: Profiling
type: docs
description: ""
author: HUATUO Team
date: 2026-06-18
weight: 4
---

## Flame Graph Formats

In profiling, **collapsed** and **flamegraph** are the two most common formats, corresponding to the "raw data" and "visual view" layers respectively.

### Collapsed Format

#### Standard Syntax and Format

The collapsed format (also called folded stacks) was defined by Brendan Gregg and serves as the **raw text input format** for flame graphs. Each line represents a unique call stack and its sample count.

**Basic rule:**

```text
frame1;frame2;frame3;...;frameN COUNT
```

| Component | Description |
|-----------|-------------|
| `frame1` | Stack bottom (entry/root frame), e.g. `main`, `start_thread` |
| `;` | Frame separator (semicolon) |
| `frameN` | Stack top (currently executing frame, i.e. the sampled point) |
| `COUNT` | Sample count (integer), separated from the stack frames by a **space** |

**Format details:**

- One unique call stack per line; samples with the same stack path have their counts merged
- Frame order: left to right is **root → leaf** (call chain direction)
- Blank lines and lines starting with `#` are treated as comments and ignored during parsing
- The semantics of COUNT depend on the analysis mode: for CPU sampling it is the number of samples, for memory allocation it is the number of bytes allocated, for lock analysis it is the contention time in milliseconds

**Extended specification:**

Some profiling tools (e.g. async-profiler) add **frame type annotations** on top of the standard format to identify the runtime category of a frame:

```text
frameName_{type} COUNT
```

| Annotation | Meaning | Description |
|------------|---------|-------------|
| `_[j]` | JIT compiled Java | Java method after JIT compilation |
| `_[i]` | Interpreted Java | Java method executed by the interpreter |
| `_[k]` | Kernel | Kernel-mode frame |
| `_[n]` | Native C/C++ | Native C/C++ frame |
| `_[t]` | Thread | Thread frame |

Additionally, some tools support a **weighted collapsed format** for differential flame graphs:

```text
frame1;frame2;frameN WEIGHT
```

Where `WEIGHT` is a floating-point number representing the weight of the stack rather than a simple count.

#### Sample Examples

**CPU profiling example (data from the async-profiler official documentation):**

```text
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult 21
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeInt 1
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeInt;java/io/ByteArrayOutputStream.write 5
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF 12
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF;java/lang/String.length 3
FileConverter.main;FileConverter.convertFile;FileConverter.saveResult;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.writeUTF;java/io/DataOutputStream.write 6
start_thread;thread_native_entry;Thread::call_run;VMThread::run;VMThread::inner_execute;VMThread::evaluate_operation;VM_Operation::evaluate;VM_GenCollectForAllocation::doit;GenCollectedHeap::satisfy_failed_allocation;GenCollectedHeap::do_collection;GenCollectedHeap::collect_generation;DefNewGeneration::collect;DefNewGeneration::FastEvacuateFollowersClosure::do_void 12
```

**Example with frame type annotations (async-profiler extension):**

```text
Main.run_[j];Service.process_[j];DAO.query_[j];mysql_real_query_[n] 45
Main.run_[j];Service.process_[j];DAO.query_[j];recv_[k] 18
```

#### Core Use Cases

| Use Case | Description |
|----------|-------------|
| **Flame graph generation** | Standard input format for visualization tools like `flamegraph.pl` and `inferno` |
| **Differential analysis** | Compare two collapsed files to produce a red-blue differential flame graph for detecting performance regressions |
| **Programmatic processing** | Plain text format suitable for custom aggregation and filtering with `awk`, `sed`, Python, etc. |
| **Cross-tool interoperability** | Universal standard defined by Brendan Gregg; supported by virtually all flame graph toolchains |
| **Long-term storage** | Compact text format suitable for archiving and version comparison |
| **CI/CD integration** | Enables automated collection, diffing, and threshold-based regression detection in pipelines |

**Generation command example:**

```bash
# Using async-profiler as an example
asprof -d 30 -f profile.collapsed -o collapsed <PID>
```

---

### Flamegraph Format

#### Standard Syntax and Format

The flamegraph format is a **self-contained HTML file** with embedded SVG visualization and JavaScript interaction logic, which can be opened directly in a browser.

**Structural composition:**

```bash
flamegraph.html
├── HTML skeleton + CSS styles
├── SVG flame graph body
│   ├── <g> rectangle block for each frame
│   │   ├── <title> frame name + sample count/percentage
│   │   └── <rect> position, width, height, color
│   └── ...
├── JavaScript interaction logic
│   ├── Click to zoom (zoom into subtree)
│   ├── Search & highlight
│   ├── Tooltip on hover
│   └── Reset zoom
└── Metadata (title, total samples, etc.)
```

**Visual encoding rules:**

| Dimension | Encoding Meaning |
|-----------|-----------------|
| **X axis** | Call stack frames sorted alphabetically (**not a timeline**); width proportional to sample count |
| **Y axis** | Call stack depth; bottom is the root frame, top is the leaf frame |
| **Frame width** | Proportion of samples where this frame appears in the stack; wider frames consume more resources |
| **Frame color** | Identifies the frame type (see table below) |

**Frame color specification (based on async-profiler):**

> **Note**: Flame graph color schemes are not a cross-tool standard. The original `flamegraph.pl` by Brendan Gregg uses random warm tones with no semantic meaning; `perf`/`bpftrace` typically colors by DSO or uses random colors; async-profiler colors by frame type semantics. The following is the async-profiler color specification:

| Color | Frame Type | Description |
|-------|------------|-------------|
| 🟢 Green | Java (interpreted) | Java method executed by the interpreter |
| 🟡 Yellow/Orange | Java (JIT compiled) | Java method after JIT compilation |
| 🔴 Red | C/C++ (native) | Native C/C++ code |
| 🔵 Blue | Kernel | Kernel-mode code |
| ⬜ Gray | Other/Unknown | Other types or unknown frames |

**Extended features (based on async-profiler):**

- **Icicle Graph**: Displays the call chain top-down (root at the top), which better suits top-down reading habits. Toggle via the `--reverse` option or the Reverse button in the browser
- **Multi-thread view**: Call stacks from different threads are displayed side by side at the root level
- **Search highlighting**: Matching frames are highlighted in purple; non-matching frames are dimmed
- **Sample info tooltip**: Hover to display frame name, sample count, and percentage of total samples
- **Cutoff frames**: Frames marked as `[...]` indicate stack truncation (e.g. due to stack depth limits)

#### Sample Examples

**Generation command example:**

```bash
# Using async-profiler as an example
asprof -d 30 -f flamegraph.html <PID>
```

**Interactive operations:**

- **Click a frame**: Zoom to make the frame full-width, showing only its subtree
- **Search box**: Enter a keyword; matching frames are highlighted
- **Hover**: Display frame name, sample count, and percentage
- **Reset Zoom**: Restore the global view

#### Core Use Cases

| Use Case | Description |
|----------|-------------|
| **Hotspot identification** | Visually identify the widest frame blocks to quickly find the code paths consuming the most CPU/memory |
| **Root cause analysis** | Trace upward from leaf frames to understand the call chain context of resource consumption |
| **Team collaboration** | HTML files can be shared directly; viewable in a browser with no additional tools required |
| **Optimization verification** | Generate flame graphs before and after optimization; compare frame width changes to verify effectiveness |
| **Non-specialist friendly** | Visual form is easier to understand for non-performance engineers, facilitating cross-team communication |

---

### Format Comparison

| Dimension | Collapsed | Flamegraph |
|-----------|-----------|------------|
| Format type | Plain text | HTML + SVG |
| Human readability | Medium (requires understanding stack frame syntax) | High (visual, intuitive) |
| Machine readability | High (easy to parse, easy to diff) | Low (requires parsing HTML/SVG) |
| Interactivity | None | Supports zoom, search, tooltip |
| File size | Very small (KB scale) | Larger (hundreds of KB to MB scale) |
| Toolchain dependency | None (plain text) | Browser |
| Differential analysis | Natively supported (diff two files) | Requires conversion to collapsed first |
| Typical use case | Programmatic processing, CI comparison, archiving | Manual analysis, team sharing, presentation |

**Typical workflow:**

```bash
Collect ──► collapsed ──► flamegraph.html (manual analysis)
                   │
                   ├──► Differential flame graph (regression detection)
                   ├──► Custom aggregation scripts
                   └──► Archive storage
```

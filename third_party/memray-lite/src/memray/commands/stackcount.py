import argparse
from collections import defaultdict
from typing import Dict, Tuple, List

from memray._errors import MemrayCommandError
from memray._memray import FileReader
from memray._memray import AllocatorType
from memray.reporters.frame_tools import is_frame_interesting


class StackcountCommand:
    """Aggregate allocations by stack and print textual counts (stackcount-like)."""

    def prepare_parser(self, parser: argparse.ArgumentParser) -> None:
        parser.add_argument("results", help="Results of the tracker run (.bin file)")
        parser.add_argument(
            "-n",
            "--top",
            type=int,
            default=0,
            help="Show only top N stacks by total bytes (0 = all)",
        )
        parser.add_argument(
            "-m",
            "--merge-threads",
            action="store_true",
            default=True,
            help="Merge allocations across threads (default: true)",
        )
        parser.add_argument(
            "--native",
            action="store_true",
            help="Use native or hybrid stacks if available (otherwise Python)",
        )
        parser.add_argument(
            "--hybrid",
            action="store_true",
            help="Use hybrid stacks (Python merged into native) if available",
        )
        parser.add_argument(
            "--max-frames",
            type=int,
            default=0,
            help="Limit frames per stack from the leaf; 0 = no limit",
        )
        parser.add_argument(
            "--metric",
            choices=["bytes", "count"],
            default="bytes",
            help="Aggregate and sort by 'bytes' or 'count' (default: bytes)",
        )

    def run(self, args: argparse.Namespace, parser: argparse.ArgumentParser) -> None:
        try:
            reader = FileReader(args.results, report_progress=False)
        except OSError as e:
            raise MemrayCommandError(
                f"Failed to open results file {args.results}: {e}", exit_code=1
            )

        # Choose which stack accessor to use
        def get_stack(alloc):
            if args.hybrid:
                return alloc.hybrid_stack_trace
            if args.native:
                return alloc.native_stack_trace
            return alloc.stack_trace

        max_stacks = args.max_frames if args.max_frames and args.max_frames > 0 else None

        # Aggregate by stack key
        totals_by_key: Dict[Tuple[Tuple[str, str, int], ...], Tuple[int, int]] = defaultdict(lambda: (0, 0))

        # Iterate all allocations; FileReader handles aggregation for HWM/leaks in other APIs,
        # but here we want raw allocations to approximate stackcount behavior.
        for alloc in reader.get_allocation_records():
            # Skip deallocation records; stacks are not available for frees
            if alloc.allocator in (AllocatorType.FREE, AllocatorType.MUNMAP):
                continue
            # Get frames, filter out CPython internals to make stacks more readable
            if max_stacks is None:
                frames: List[Tuple[str, str, int]] = get_stack(alloc)() or []
            else:
                frames: List[Tuple[str, str, int]] = get_stack(alloc)(max_stacks) or []
            filtered = [f for f in frames if is_frame_interesting((f[0], f[1], f[2]))]
            key = tuple(filtered)
            total_bytes, num = totals_by_key[key]
            totals_by_key[key] = (total_bytes + (alloc.size or 0), num + (alloc.n_allocations or 1))

        # Sort by selected metric
        if args.metric == "count":
            items = sorted(
                totals_by_key.items(), key=lambda kv: (kv[1][1], kv[1][0]), reverse=True
            )
        else:
            items = sorted(
                totals_by_key.items(), key=lambda kv: (kv[1][0], kv[1][1]), reverse=True
            )
        if args.top and args.top > 0:
            items = items[: args.top]

        # Print similar block format to bcc/stackcount
        for stack, (total_bytes, num_allocs) in items:
            if not stack:
                print("  [unknown]")
            else:
                for (func, file, line) in stack:
                    # Match bcc style: function name per line; include file:line if present
                    if file and line:
                        print(f"  {func}")
                    else:
                        print(f"  {func}")
            value = total_bytes if args.metric == "bytes" else num_allocs
            print(f"    {value}")
            print()

        reader.close()

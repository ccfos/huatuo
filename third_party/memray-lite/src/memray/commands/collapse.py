import argparse
from collections import defaultdict
from typing import Dict, List, Tuple

from memray._errors import MemrayCommandError
from memray._memray import AllocatorType
from memray._memray import FileReader
from memray.reporters.frame_tools import is_frame_interesting


Frame = Tuple[str, str, int]


class CollapseCommand:
    """Emit collapsed stack samples for downstream tooling."""

    def prepare_parser(self, parser: argparse.ArgumentParser) -> None:
        parser.add_argument("results", help="Results of the tracker run (.bin file)")
        parser.add_argument(
            "-n",
            "--top",
            type=int,
            default=0,
            help="Show only top N stacks by the selected metric (0 = all)",
        )
        parser.add_argument(
            "--native",
            action="store_true",
            help="Use native stacks if present (overridden by --hybrid)",
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
        parser.add_argument(
            "--separator",
            default=";",
            help="String used between frames in the collapsed output",
        )

    def run(self, args: argparse.Namespace, parser: argparse.ArgumentParser) -> None:
        if not args.separator:
            parser.error("--separator cannot be empty")

        try:
            reader = FileReader(args.results, report_progress=False)
        except OSError as e:
            raise MemrayCommandError(
                f"Failed to open results file {args.results}: {e}", exit_code=1
            )

        try:
            entries = _aggregate_allocations(reader, args)
        finally:
            reader.close()

        if args.metric == "count":
            entries.sort(key=lambda kv: (kv[1][1], kv[1][0]), reverse=True)
        else:
            entries.sort(key=lambda kv: (kv[1][0], kv[1][1]), reverse=True)

        if args.top and args.top > 0:
            entries = entries[: args.top]

        for stack, (total_bytes, num_allocs) in entries:
            value = total_bytes if args.metric == "bytes" else num_allocs
            collapsed = _format_stack(stack, args.separator)
            print(f"{collapsed} {value}")


def _aggregate_allocations(
    reader: FileReader, args: argparse.Namespace
) -> List[Tuple[Tuple[Frame, ...], Tuple[int, int]]]:
    def get_stack_accessor(alloc):
        if args.hybrid:
            return alloc.hybrid_stack_trace
        if args.native:
            return alloc.native_stack_trace
        return alloc.stack_trace

    max_frames = args.max_frames if args.max_frames and args.max_frames > 0 else None

    totals_by_key: Dict[Tuple[Frame, ...], Tuple[int, int]] = defaultdict(lambda: (0, 0))

    for alloc in reader.get_allocation_records():
        if alloc.allocator in (AllocatorType.FREE, AllocatorType.MUNMAP):
            continue

        accessor = get_stack_accessor(alloc)
        if max_frames is None:
            frames: List[Frame] = accessor() or []
        else:
            frames = accessor(max_frames) or []

        filtered = tuple(
            frame for frame in frames if is_frame_interesting((frame[0], frame[1], frame[2]))
        )

        total_bytes, num_allocs = totals_by_key[filtered]
        totals_by_key[filtered] = (
            total_bytes + (alloc.size or 0),
            num_allocs + (alloc.n_allocations or 1),
        )

    return list(totals_by_key.items())


def _format_stack(stack: Tuple[Frame, ...], separator: str) -> str:
    if not stack:
        return "[unknown]"

    def render_frame(func: str, file: str, line: int) -> str:
        name = func or "[unknown]"
        if file and line:
            return f"{name} ({file}:{line})"
        return name

    return separator.join(render_frame(*frame) for frame in stack)

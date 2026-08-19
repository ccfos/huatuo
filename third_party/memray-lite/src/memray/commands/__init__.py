import argparse
import logging
import sys
import textwrap
from typing import List
from typing import Optional

from memray._version import __version__

try:
    from typing import Protocol
except ImportError:
    from typing_extensions import Protocol

from memray._errors import MemrayCommandError
from memray._errors import MemrayError
from memray._memray import set_log_level

from . import attach
from . import collapse
from . import stackcount

_EPILOG = textwrap.dedent(
    """\
    Please submit feedback, ideas, and bug reports by filing a new issue at
    https://github.com/bloomberg/memray/issues
    """
)

_DESCRIPTION = textwrap.dedent(
    """\
    Memory profiler for running Python processes

    Use `memray attach` to record allocations from an existing PID, then feed the
    resulting trace into `memray collapse` or `memray stackcount` for text output.
    """
)


class Command(Protocol):
    def prepare_parser(self, parser: argparse.ArgumentParser) -> None:
        ...

    def run(self, args: argparse.Namespace, parser: argparse.ArgumentParser) -> None:
        ...


_COMMANDS: List[Command] = [
    collapse.CollapseCommand(),
    stackcount.StackcountCommand(),
    attach.AttachCommand(),
    attach.DetachCommand(),
]


def get_argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=_DESCRIPTION,
        prog="memray",
        formatter_class=argparse.RawTextHelpFormatter,
        epilog=_EPILOG,
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="count",
        default=0,
        help="Increase verbosity. Option is additive and can be specified up to 3 times",
    )
    parser.add_argument(
        "-V",
        "--version",
        action="version",
        version=__version__,
        help="Displays the current version of Memray",
    )

    # Python 3.6: argparse.add_subparsers doesn't support required=
    if sys.version_info[:2] >= (3, 7):
        subparsers = parser.add_subparsers(
            help="Mode of operation",
            dest="command",
            required=True,
        )
    else:
        subparsers = parser.add_subparsers(
            help="Mode of operation",
            dest="command",
        )

    for command in _COMMANDS:
        # Extract the CLI command name from the classes' names
        assert command.__class__.__name__.endswith("Command")
        name = command.__class__.__name__[: -len("Command")].lower()

        # Add the subcommand
        command_parser = subparsers.add_parser(
            name, help=command.__doc__, description=command.__doc__, epilog=_EPILOG
        )
        command_parser.set_defaults(entrypoint=command.run)
        command.prepare_parser(command_parser)

    return parser


def determine_logging_level_from_verbosity(
    verbose_level: int,
) -> int:  # pragma: no cover
    if verbose_level == 0:
        return logging.WARNING
    elif verbose_level == 1:
        return logging.INFO
    else:
        return logging.DEBUG


def main(args: Optional[List[str]] = None) -> int:
    if args is None:
        args = sys.argv[1:]

    parser = get_argument_parser()
    arg_values = parser.parse_args(args=args)
    # Emulate required=True for subparsers on Python < 3.7
    if getattr(arg_values, "command", None) is None:
        parser.print_help()
        return 2
    set_log_level(determine_logging_level_from_verbosity(arg_values.verbose))

    try:
        arg_values.entrypoint(arg_values, parser)
    except MemrayCommandError as e:
        print(e, file=sys.stderr)
        return e.exit_code
    except MemrayError as e:
        print(e, file=sys.stderr)
        return 1
    else:
        return 0

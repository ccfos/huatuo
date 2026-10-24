#!/usr/bin/env python3

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Shared network workloads; assertions and namespace ownership stay in Bash."""

import argparse
import signal
import socket
import struct
import sys
import time


def checksum(data):
    if len(data) % 2:
        data += b"\0"
    total = sum(struct.unpack(f"!{len(data) // 2}H", data))
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return ~total & 0xFFFF


def packet(source, destination, source_port, destination_port, protocol, options, ttl):
    source_bytes = socket.inet_aton(source)
    destination_bytes = socket.inet_aton(destination)
    if protocol == socket.IPPROTO_TCP:
        segment = struct.pack("!HHIIBBHHH", source_port, destination_port,
                              1, 0, 0x50, 0x02, 8192, 0, 0)
        pseudo = struct.pack("!4s4sBBH", source_bytes, destination_bytes, 0,
                             protocol, len(segment))
        segment = segment[:16] + struct.pack("!H", checksum(pseudo + segment)) + segment[18:]
    else:
        # A zero UDP checksum is valid for IPv4.
        segment = struct.pack("!HHHH", source_port, destination_port, 8, 0)

    header_length = 20 + len(options)
    header = struct.pack("!BBHHHBBH4s4s", 0x40 | (header_length // 4), 16,
                         header_length + len(segment), 0x1315, 0x4000, ttl,
                         protocol, 0, source_bytes, destination_bytes)
    # Linux fills the IP checksum for IP_HDRINCL packets.
    return header + options + segment


def send_frames(sock, destination, frames):
    while True:
        for frame in frames:
            sock.sendto(frame, (destination, 0))
        time.sleep(0.1)


def send_ipv4_options(args):
    frames = [
        packet(args.warmup_source, args.destination, args.source_port, args.destination_port,
               socket.IPPROTO_UDP, b"", 32),
        packet(args.source, args.destination, args.source_port, args.destination_port,
               socket.IPPROTO_TCP, b"\0" * 4, 64),
        packet(args.source, args.destination, args.source_port, args.destination_port,
               socket.IPPROTO_UDP, b"\1\0\0\0", 64),
        packet(args.source, args.destination, args.source_port, args.destination_port,
               socket.IPPROTO_TCP, b"\1" * 39 + b"\0", 64),
    ]
    with socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_RAW) as sock:
        sock.setsockopt(socket.IPPROTO_IP, socket.IP_HDRINCL, 1)
        send_frames(sock, args.destination, frames)


def send_icmpv6_echo(args):
    frame = struct.pack("!BBHHH", 128, 0, 0, args.identifier, args.sequence) + b"huatuo"
    # Linux computes the mandatory checksum for IPPROTO_ICMPV6 raw sockets.
    with socket.socket(socket.AF_INET6, socket.SOCK_RAW, socket.IPPROTO_ICMPV6) as sock:
        sock.bind((args.source, 0))
        send_frames(sock, args.destination, [frame])


def connect_tcp(args):
    # Raw SYN packets do not create a retransmitting kernel TCP socket.
    while True:
        family = socket.AF_INET6 if ":" in args.source else socket.AF_INET
        with socket.socket(family, socket.SOCK_STREAM) as sock:
            sock.bind((args.source, 0))
            sock.settimeout(3)
            try:
                sock.connect((args.destination, args.destination_port))
            except (socket.timeout, ConnectionRefusedError):
                pass


def uint16(value):
    number = int(value)
    if not 0 <= number <= 65535:
        raise argparse.ArgumentTypeError("value must be between 0 and 65535")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    workloads = parser.add_subparsers(dest="workload")
    workloads.required = True
    ipv4 = workloads.add_parser("ipv4-options", help="send warm-up UDP and TCP/UDP EOL options")
    ipv4.add_argument("--warmup-source", required=True)
    ipv4.add_argument("--source-port", type=uint16, required=True)
    ipv4.add_argument("--destination-port", type=uint16, required=True)
    ipv4.set_defaults(send=send_ipv4_options)
    icmpv6 = workloads.add_parser("icmpv6-echo", help="send IPv6 Echo requests")
    icmpv6.add_argument("--identifier", type=uint16, required=True)
    icmpv6.add_argument("--sequence", type=uint16, required=True)
    icmpv6.set_defaults(send=send_icmpv6_echo)
    tcp = workloads.add_parser("tcp-connect", help="repeat TCP connection attempts")
    tcp.add_argument("--destination-port", type=uint16, required=True)
    tcp.set_defaults(send=connect_tcp)
    for workload in (ipv4, icmpv6, tcp):
        workload.add_argument("--source", required=True)
        workload.add_argument("--destination", required=True)
    args = parser.parse_args()
    # The harness stops continuous traffic with SIGTERM and checks its status.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    args.send(args)


if __name__ == "__main__":
    main()

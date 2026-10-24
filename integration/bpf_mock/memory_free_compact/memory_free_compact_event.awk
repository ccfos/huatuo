# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The HuaTuo Authors.
/^DECLARE_EVENT_CLASS\(mm_vmscan_direct_reclaim_(begin|end)_template,/ { copying = 1; classes++ }
/^DEFINE_EVENT\(mm_vmscan_direct_reclaim_(begin|end)_template, mm_vmscan_direct_reclaim_(begin|end),/ {
    copying = 1; events++; event = ($0 ~ /_begin,/ ? "begin" : "end")
}
copying {
    gsub(/mm_vmscan_direct_reclaim_begin_template/, "mm_vmscan_direct_reclaim_begin_mock_template")
    gsub(/mm_vmscan_direct_reclaim_end_template/, "mm_vmscan_direct_reclaim_end_mock_template")
    sub(/mm_vmscan_direct_reclaim_begin,/, "mm_vmscan_direct_reclaim_begin_mock,")
    sub(/mm_vmscan_direct_reclaim_end,/, "mm_vmscan_direct_reclaim_end_mock,")
    print
    if (event && /TP_PROTO/) {
        params = $0; sub(/^.*TP_PROTO\(/, "", params); sub(/\).*/, "", params)
        count = split(params, args, ","); call = ""
        for (i = 1; i <= count; i++) {
            type = args[i]; sub(/[A-Za-z_][A-Za-z_0-9]*$/, "", type)
            call = call (i == 1 ? "" : ", ") "(" type "){0}"
        }
        calls = calls "#define CALL_RECLAIM_" toupper(event) "() trace_mm_vmscan_direct_reclaim_" event "_mock(" call ")\n"
    }
    if (/^\);/) { copying = 0; event = "" }
}
END {
    if (classes != 2 || events != 2 || copying) {
        print "direct reclaim tracepoint declarations missing or incomplete" > "/dev/stderr"; exit 1
    }
    print calls
}

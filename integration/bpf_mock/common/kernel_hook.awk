# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The HuaTuo Authors.

# Only plain parameter declarations are accepted; fail on unfamiliar syntax.
function parameter_type(parameter) {
    gsub(/^[ \t]+|[ \t]+$/, "", parameter)
    if (parameter ~ /[()\[\]]/) {
        print "unsupported parameter: " parameter > "/dev/stderr"
        exit 1
    }
    if (parameter ~ /\* *[A-Za-z_][A-Za-z_0-9]*$/ ||
        (parameter ~ / / && parameter !~ / (long|int|short|char|bool)$/))
        sub(/[A-Za-z_][A-Za-z_0-9]*$/, "", parameter)
    gsub(/[ \t]+$/, "", parameter)
    return parameter
}
$0 ~ "^(extern[ \t]+)?(void|unsigned long|enum compact_result)[ \t]+" symbol "\\(" {
    found = 1
    declaration = $0
    if (/;/) { complete = 1; exit }
    next
}
found { declaration = declaration " " $0 }
found && /;/ { complete = 1; exit }
END {
    if (!found) {
        if (!optional) { print "declaration missing: " symbol > "/dev/stderr"; exit 1 }
        exit
    }
    if (!complete) { print "incomplete declaration: " symbol > "/dev/stderr"; exit 1 }
    sub(/^extern[ \t]+/, "", declaration)
    result = declaration
    sub("[ \t]+" symbol ".*", "", result)
    sub(/^[^(]*\(/, "", declaration)
    sub(/\).*/, "", declaration)
    count = split(declaration, parameters, ",")
    signature = ""; call = ""; supplied = ""
    for (i = 1; i <= count; i++) {
        type = parameter_type(parameters[i])
        signature = signature (i == 1 ? "" : ", ") type " mock_arg" i
        call = call (i == 1 ? "" : ", ") (i <= inputs ? "arg" i : "(" type "){0}")
        if (i <= inputs) supplied = supplied (i == 1 ? "" : ", ") "arg" i
    }
    print "#define HAVE_" symbol " 1"
    print result " noinline __noclone " symbol "_mock(" signature ");"
    print result " noinline __noclone " symbol "_mock(" signature ") { " body "; }"
    print "_Static_assert(__builtin_types_compatible_p(typeof(&" symbol "), typeof(&" symbol "_mock)), \"mock signature mismatch\");"
    print "#define CALL_" symbol "(" supplied ") " symbol "_mock(" call ")"
}

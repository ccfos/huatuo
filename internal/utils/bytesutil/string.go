package bytesutil

// ToString converts a C-style null-terminated byte slice to a Go string.
// It stops at the first zero byte, avoiding the double allocations caused by
// string()+strings.Trim.
func ToString(b []byte) string {
	for i, v := range b {
		if v == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

//go:build !windows

package tools

// decodeConsoleOutput is the Unix side: a Linux sandbox writes UTF-8 already, so the captured bytes pass through unchanged.
// The counterpart on Windows has to decode the OEM code page first (see console_output_windows.go).
func decodeConsoleOutput(b []byte) string {
	return string(b)
}

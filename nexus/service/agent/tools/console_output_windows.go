//go:build windows

package tools

import (
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// cpOEMCP is the CP_OEMCP pseudo code page: "the system's OEM code page". That is the code page a console program encodes
// redirected output with, so passing it - instead of a number - lets Windows pick the right table (936 on a Chinese system,
// 932 on a Japanese one, 437 on a US one) and keeps this file free of a code-page table of its own.
const cpOEMCP = 1

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procGetOEMCP            = kernel32.NewProc("GetOEMCP")
)

// decodeConsoleOutput turns a command's captured output into UTF-8.
//
// Why it is needed: on Windows a console program does not write UTF-8 - `cmd`'s own commands (dir / type / ...) write the
// system OEM code page, so `dir` on a Chinese system emits GBK. Nothing along the path carries raw bytes once the result
// becomes JSON for the model or the chat window: invalid UTF-8 turns into U+FFFD there, which is exactly the wall of `�`
// a user sees. Decoding here, where the bytes are still intact, is the only point that does not lose them.
//
// The decode is skipped when the bytes already are UTF-8: every non-Windows sandbox writes UTF-8, and a Windows program
// may opt into it (python with PYTHONUTF8, for example), so "valid UTF-8" is the reliable signal - not the OS.
//
// Known limit: one stream that mixes encodings (a GBK `dir` line followed by a UTF-8 tool's own output) is decoded whole,
// so the UTF-8 part comes out wrong. Holding bytes that are not text at all (a binary dump) decodes into nonsense as well;
// both are reported as what they are rather than silently replaced.
func decodeConsoleOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	text, err := fromOEMCodePage(b)
	if err != nil || !utf8.ValidString(text) {
		// Undecodable: hand back the raw bytes. The JSON boundary still shows U+FFFD, but that is honest - a guess would
		// be indistinguishable from real text.
		return string(b)
	}
	return text
}

// fromOEMCodePage converts OEM-code-page bytes into a Go string (the API returns UTF-16, which Go handles natively).
func fromOEMCodePage(b []byte) (string, error) {
	width, _, callErr := procMultiByteToWideChar.Call(
		cpOEMCP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		0, 0)
	if width == 0 {
		return "", callErr
	}
	wide := make([]uint16, width)
	written, _, callErr := procMultiByteToWideChar.Call(
		cpOEMCP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&wide[0])), width)
	if written == 0 {
		return "", callErr
	}
	return string(utf16.Decode(wide[:written])), nil
}

// oemCodePage reports the system OEM code page (936 on a Chinese Windows). Only the test uses it, to skip its GBK fixture on
// a machine where the same bytes mean something else.
func oemCodePage() uint32 {
	cp, _, _ := procGetOEMCP.Call()
	return uint32(cp)
}

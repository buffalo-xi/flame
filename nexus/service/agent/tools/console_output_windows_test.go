//go:build windows

package tools

import (
	"testing"
)

// A Windows console program writes redirected output in the system OEM code page, not UTF-8 (cmd's own `dir` on a Chinese
// system emits GBK). Those bytes are what the user sees as a wall of U+FFFD, so the decode has to happen before the result
// becomes text. The fixture is GBK, so it is skipped where the same bytes mean something else.
func TestDecodeConsoleOutputFromOEMCodePage(t *testing.T) {
	if cp := oemCodePage(); cp != 936 {
		t.Skipf("system OEM code page is %d, not 936 (this fixture is GBK)", cp)
	}

	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4} // 中文
	if got := decodeConsoleOutput(gbk); got != "中文" {
		t.Fatalf("decodeConsoleOutput(GBK 中文) = %q, want the decoded text", got)
	}

	// A program that opted into UTF-8 must not be decoded twice: valid UTF-8 is the signal, not the OS.
	if got := decodeConsoleOutput([]byte("中文")); got != "中文" {
		t.Fatalf("valid UTF-8 must pass through, got %q", got)
	}
	// Plain ASCII is valid UTF-8 as well, and the common case: no conversion pass at all.
	if got := decodeConsoleOutput([]byte("dir C:\\")); got != "dir C:\\" {
		t.Fatalf("ASCII must pass through, got %q", got)
	}
	if got := decodeConsoleOutput(nil); got != "" {
		t.Fatalf("empty output = %q, want an empty string", got)
	}
}

// The buffer is where exec / run_skill_script / custom tools all end up taking the text, so the decode has to sit there
// rather than at one call site.
func TestLimitedBufferDecodesConsoleOutput(t *testing.T) {
	if cp := oemCodePage(); cp != 936 {
		t.Skipf("system OEM code page is %d, not 936 (this fixture is GBK)", cp)
	}
	b := newLimitedBuffer(1024)
	if _, err := b.Write([]byte{0x2D, 0x2D, 0x20, 0xD6, 0xD0, 0xCE, 0xC4}); err != nil { // "-- 中文"
		t.Fatalf("write: %v", err)
	}
	if got := b.String(); got != "-- 中文" {
		t.Fatalf("limitedBuffer.String() = %q, want the decoded text", got)
	}
}

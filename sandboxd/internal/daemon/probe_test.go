package daemon

import (
	"strings"
	"testing"
)

// formatOSRelease only reformats: it picks the known keys and drops the noisy lines, and draws **no conclusions**.
// That boundary is deliberate — the moment this starts "interpreting" (for example "does this system have yum"), it is
// drawing conclusions on the agent's behalf, which the agent should infer from the system name itself.
func TestFormatOSRelease(t *testing.T) {
	// A real sample (/etc/os-release inside a Debian 13 container), and the very one that triggered this change: of a
	// dozen lines only PRETTY_NAME means anything to the agent.
	debian := `PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
NAME="Debian GNU/Linux"
VERSION_ID="13"
VERSION="13 (trixie)"
VERSION_CODENAME=trixie
DEBIAN_VERSION_FULL=13.7
ID=debian
HOME_URL="https://www.debian.org/"
SUPPORT_URL="https://www.debian.org/support"
BUG_REPORT_URL="https://bugs.debian.org/"
`

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"real sample → one line", debian, "OS: Debian GNU/Linux 13 (trixie)"},
		{"unquoted value", "PRETTY_NAME=Alpine Linux v3.20\nID=alpine\n", "OS: Alpine Linux v3.20"},
		{"no PRETTY_NAME → fall back to ID + version", "ID=alpine\nVERSION_ID=3.20\n", "OS: alpine 3.20"},
		{"ID only", "ID=debian\n", "OS: debian"},
		{
			// No known key recognised: better to hand it over verbatim than to swallow the fact.
			// When os-release is missing, `cat` fails and the unavailable path is taken; this guards the case where
			// the file is there but does not look like os-release.
			"unrecognised → returned verbatim",
			"some unexpected\ntext here\n",
			"some unexpected\ntext here",
		},
		{"empty input → verbatim (empty string)", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatOSRelease(tc.in); got != tc.want {
				t.Errorf("formatOSRelease() = %q, want %q", got, tc.want)
			}
		})
	}
}

// firstLine: keep only the first line of a failure message, with a cap.
// The cause was the luarocks probe failing on every read-only rootfs and pouring a 500-byte Lua stack traceback into
// Environment.Text — breaking the "one line per entry" shape without adding information.
func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single line verbatim", "exit 1: permission denied\n", "exit 1: permission denied"},
		{"multi-line keeps only the first", "boom\nstack traceback:\n\t[C]: in function 'x'\n", "boom"},
		{"empty output gets a placeholder", "\n\n", "no output"},
		{"over-long output truncated", strings.Repeat("a", 300), strings.Repeat("a", 200) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in); got != tc.want {
				t.Errorf("firstLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

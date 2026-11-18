package sessions

import (
	"errors"
	"testing"
)

// ParseID 是所有「把客户端给的 sid 交给 SQL」的必经之路。
// 它挡住的正是 PG 的 22P02（invalid input syntax for type uuid）。
func TestParseID(t *testing.T) {
	const valid = "a0000000-0000-4000-8000-000000000010"

	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"合法", valid, valid, nil},
		{"前后空白", "  " + valid + "\t", valid, nil},
		{"空串", "", "", ErrSessionRequired},
		{"纯空白", "   ", "", ErrSessionRequired},
		{"非 UUID", "not-a-uuid", "", ErrSessionInvalid},
		{"少一段", "a0000000-0000-4000-8000", "", ErrSessionInvalid},
		{"带路径的输入", "../../etc/passwd", "", ErrSessionInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseID(c.in)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				if got != "" {
					t.Errorf("失败时不该返回 id, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// 三个哨兵错误必须是可区分的：入口层据此给不同的协议错误码
// （InvalidParams vs InternalError），但「不存在」与「不是你的」共用同一个。
func TestSessionErrorsAreDistinct(t *testing.T) {
	all := []error{ErrSessionRequired, ErrSessionInvalid, ErrSessionForbidden}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Errorf("%v 与 %v 不应该是同一个错误", all[i], all[j])
			}
		}
	}
}

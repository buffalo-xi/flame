package sandbox

import (
	"context"
	"strings"
	"testing"
)

// 启动意图校验在 Dial 之前生效 —— 所以这些用例不需要 sandboxd 在跑。
//
// 判据必须与 sandboxd 的 buildExecArgv 同口径：本侧放宽「Argv 必须非空」是为了让
// 语言路径能过（Lang 非空时 argv 本就该为空），而不是放松校验。
func TestRemoteStartIntentGuards(t *testing.T) {
	cases := []struct {
		name string
		spec *Spec
		want string
	}{
		{"两者都空", &Spec{}, "empty argv"},
		{"语言路径缺脚本", &Spec{Lang: "python"}, "requires a script"},
		{
			"语言路径与 argv 互斥",
			&Spec{Lang: "python", Script: "/skill/main.py", Argv: []string{"lua", "/x.lua"}},
			"mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb, err := NewRemote(RemoteOptions{Endpoint: "ws://127.0.0.1:1/v1/exec"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = sb.Start(context.Background(), tc.spec)
			if err == nil {
				t.Fatal("应拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want 含 %q", err, tc.want)
			}
		})
	}
}

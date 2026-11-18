package tools

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
	"flame/tools/sandbox"
)

// runStubSandbox 捕获 Spec 与 stdin，用 out / wait 决定进程行为。
//
// 不落真进程：执行内核的判定（超限、遮蔽、JSON 解析、退出码）全在 Go 侧，
// 用桩就能覆盖；起真沙箱只会让用例依赖环境（sandboxd / 解释器是否就绪）。
type runStubSandbox struct {
	sandbox.Sandbox
	spec    *sandbox.Spec
	stdin   bytes.Buffer
	out     string
	errText string
	wait    error
}

func (s *runStubSandbox) Name() string { return "stub" }

func (s *runStubSandbox) Start(_ context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	s.spec = spec
	s.stdin.Reset()
	// stderr 由实现直写 spec.Stderr（真实实现如此），这里照做才能验证错误文案里带 stderr。
	if s.errText != "" && spec.Stderr != nil {
		_, _ = spec.Stderr.Write([]byte(s.errText))
	}
	return runStubProcess{sb: s}, nil
}

type runStubProcess struct {
	sandbox.Process
	sb *runStubSandbox
}

func (p runStubProcess) Stdin() io.WriteCloser { return nopWriteCloser{&p.sb.stdin} }

func (p runStubProcess) Stdout() io.ReadCloser {
	return io.NopCloser(strings.NewReader(p.sb.out))
}

func (p runStubProcess) Wait() error { return p.sb.wait }

type nopWriteCloser struct{ w io.Writer }

func (n nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }

func (n nopWriteCloser) Close() error { return nil }

// newRunFixture 造一个「盘上真的有工具目录」的执行输入。
// ToolsRoot / Workspace 都落在临时目录里：内核会 os.Stat 挂载源，假路径会掩盖真实错误。
func newRunFixture(t *testing.T, spec CustomToolSpec) (CustomToolRun, *runStubSandbox) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "demo"
	}
	if spec.Entry == "" {
		spec.Entry = "main.py"
	}
	root := t.TempDir()
	dir := filepath.Join(root, spec.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.Entry), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := &runStubSandbox{}
	return CustomToolRun{
		Spec:      spec,
		ToolsRoot: root,
		Workspace: t.TempDir(),
		Sandbox:   sb,
		MaxOutput: 1 << 16,
	}, sb
}

// 成功路径：结果解析、stdin 协议、隔离参数三件事一起钉住。
// stdin 是脚本唯一能拿到参数与配置的渠道，格式漂移等于所有自定义工具同时坏掉。
func TestRunCustomToolSpecSuccess(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{
		Lang:    "python",
		Configs: map[string]any{"region": "cn"},
	})
	sb.out = `{"total": 3}`

	res, err := RunCustomToolSpec(context.Background(), run, map[string]any{"month": "2026-09"})
	if err != nil {
		t.Fatalf("应成功，实际 %v", err)
	}
	m, ok := res.Result.(map[string]any)
	if !ok || m["total"] != float64(3) {
		t.Fatalf("结果解析错误: %#v", res.Result)
	}
	if res.ExitCode != 0 || res.Truncated || res.Masked {
		t.Errorf("成功路径不该带这些标记: %+v", res)
	}
	if !strings.Contains(sb.stdin.String(), `"month":"2026-09"`) {
		t.Errorf("stdin 未带上模型参数: %s", sb.stdin.String())
	}
	if !strings.Contains(sb.stdin.String(), `"region":"cn"`) {
		t.Errorf("stdin 未带上工具配置: %s", sb.stdin.String())
	}
	if sb.spec == nil || len(sb.spec.Mounts) != 2 {
		t.Fatalf("隔离参数异常: %+v", sb.spec)
	}
	if sb.spec.Mounts[1].Guest != scriptGuestDir || !sb.spec.Mounts[1].ReadOnly {
		t.Errorf("工具目录应以只读挂到 %s: %+v", scriptGuestDir, sb.spec.Mounts[1])
	}
}

// stdout 不是 JSON 时必须失败，但诊断要留下来 —— 试跑靠它显示「脚本到底打了什么」。
func TestRunCustomToolSpecNonJSONKeepsDiagnostics(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	sb.out = "debug line\n"

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Fatalf("应报 stdout 不是 JSON，实际 %v", err)
	}
	if res == nil || !strings.Contains(res.Stdout, "debug line") {
		t.Fatalf("失败时也要带回 stdout 诊断: %+v", res)
	}
}

// 非 0 退出码：错误里必须带退出码与 stderr，且结果对象照样返回（供试跑展示）。
//
// 回归点：远端沙箱返回的是 *sandbox.ExitError（remote.go FrameExit）。只判
// *exec.ExitError 时整个失败会退化成裸的 "exit status 2"——退出码变 0、stderr 全丢，
// 试跑看到的就是「失败但没有任何原因」。
func TestRunCustomToolSpecNonZeroExit(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	sb.wait = &sandbox.ExitError{Code: 2}
	sb.out = `{"ignored":true}`
	sb.errText = "参数 principal 必填：本金金额（元）\n"

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "退出码 2") {
		t.Fatalf("应报退出码 2，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "参数 principal 必填") {
		t.Fatalf("错误里应带 stderr 原文，实际 %v", err)
	}
	if res == nil || res.ExitCode != 2 {
		t.Fatalf("失败时要带回退出码 2: %+v", res)
	}
}

// 输出超限即失败（截断后就是坏 JSON，静默截断比报错难查得多）。
func TestRunCustomToolSpecTruncated(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{Lang: "python"})
	run.Spec.MaxOutput = 4
	sb.out = `{"long":"value"}`

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("应报超出上限，实际 %v", err)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("应标记 truncated: %+v", res)
	}
}

// 密钥遮蔽：输出里出现够长的配置值要被替换，并给结果打 _masked 标记。
// 这条同时守住「诊断信封里的 stdout 也遮蔽」—— 试跑会把 stdout 原样送到浏览器。
func TestRunCustomToolSpecMasksSecrets(t *testing.T) {
	run, sb := newRunFixture(t, CustomToolSpec{
		Lang:    "python",
		Configs: map[string]any{"api_key": "sk-1234567890"},
	})
	sb.out = `{"key":"sk-1234567890"}`

	res, err := RunCustomToolSpec(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("应成功，实际 %v", err)
	}
	if !res.Masked {
		t.Fatal("应标记已遮蔽")
	}
	if strings.Contains(res.Stdout, "sk-1234567890") {
		t.Errorf("stdout 诊断里仍有明文密钥: %s", res.Stdout)
	}
	m, _ := res.Result.(map[string]any)
	if m["_masked"] != true {
		t.Errorf("结果应带 _masked 提示: %#v", res.Result)
	}
}

// 必填配置缺失在执行前就报，并指出缺哪个 key —— 这是唯一能给出这个信息的地方。
func TestRunCustomToolSpecMissingRequiredConfig(t *testing.T) {
	run, _ := newRunFixture(t, CustomToolSpec{
		Lang:            "python",
		RequiredConfigs: []string{"api_key"},
	})

	_, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("应指出缺哪个配置，实际 %v", err)
	}
}

// 盘上目录不可达（挂载源缺失）要说清路径，别拖到沙箱启动才报一个 ENOENT。
func TestRunCustomToolSpecMissingToolDir(t *testing.T) {
	run, _ := newRunFixture(t, CustomToolSpec{Lang: "python"})
	run.ToolsRoot = t.TempDir() // 空目录：没有 demo/

	_, err := RunCustomToolSpec(context.Background(), run, nil)
	if err == nil || !strings.Contains(err.Error(), "不可达") {
		t.Fatalf("应报目录不可达，实际 %v", err)
	}
}

// 出网取「工具声明 ∧ 平台开通」两层：任一层没开就必须断网。
func TestRunCustomToolSpecEgressNeedsBothLayers(t *testing.T) {
	cases := []struct {
		name   string
		egress bool
		caps   map[string]bool
		want   bool
	}{
		{"声明但平台未开", true, map[string]bool{capability.Exec: true}, false},
		{"声明且平台已开", true, map[string]bool{capability.Exec: true, capability.Egress: true}, true},
		{"未声明但平台已开", false, map[string]bool{capability.Exec: true, capability.Egress: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, sb := newRunFixture(t, CustomToolSpec{Lang: "python", Egress: tc.egress})
			run.Caps = tc.caps
			sb.out = `{}`
			if _, err := RunCustomToolSpec(context.Background(), run, nil); err != nil {
				t.Fatalf("执行应成功: %v", err)
			}
			if sb.spec.Egress == nil || *sb.spec.Egress != tc.want {
				t.Fatalf("egress = %v, want %v", sb.spec.Egress, tc.want)
			}
		})
	}
}

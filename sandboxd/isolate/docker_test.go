package isolate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
)

// docker 模式「杀干净」的真机验收：Kill 之后容器必须消失。
//
// 需要本机有可用的 docker 与镜像，否则跳过：
//   - SANDBOXD_TEST_DOCKER 指定 docker 可执行文件（缺省 docker）；
//   - SANDBOXD_TEST_IMAGE  指定镜像（缺省 flame/sandbox:latest，见 sandboxd/Dockerfile）。

const defaultTestImage = "flame/sandbox:latest"

// dockerTestEnv 真机测试需要的三样东西：隔离实现、docker 可执行文件、镜像名。
type dockerTestEnv struct {
	iso   sandbox.Sandbox
	bin   string
	image string
}

// dockerForTest 构造可用的 docker 隔离实现；没有 docker / 镜像就跳过。
func dockerForTest(t *testing.T) dockerTestEnv {
	t.Helper()

	bin := os.Getenv("SANDBOXD_TEST_DOCKER")
	if bin == "" {
		bin = "docker"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("no docker binary (%s); set SANDBOXD_TEST_DOCKER to enable", bin)
	}

	image := os.Getenv("SANDBOXD_TEST_IMAGE")
	if image == "" {
		image = defaultTestImage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bin, "image", "inspect", image).Run(); err != nil {
		t.Skipf("docker image %s not available locally; set SANDBOXD_TEST_IMAGE to enable", image)
	}

	return dockerTestEnv{
		// 与 config.yaml 的 docker 段同口径，避免测的是一个不存在的强度。
		iso:   NewDocker(Options{Binary: bin, Image: image, Network: "none", ReadOnly: true, User: "1000:1000"}),
		bin:   bin,
		image: image,
	}
}

// cleanOrphansForTest 先扫一遍历史残留：计数才有意义。
func (e dockerTestEnv) cleanOrphansForTest(t *testing.T) {
	t.Helper()
	oc, ok := e.iso.(sandbox.OrphanCleaner)
	if !ok {
		t.Fatal("docker 实现应当实现 sandbox.OrphanCleaner")
	}
	n, err := oc.CleanOrphans(context.Background())
	if err != nil {
		t.Fatalf("clean orphans: %v", err)
	}
	if n > 0 {
		t.Logf("启动前回收了 %d 个残留容器", n)
	}
}

// TestDockerKillRemovesContainer Kill 之后容器必须消失，CLI 也必须收尾。
func TestDockerKillRemovesContainer(t *testing.T) {
	env := dockerForTest(t)
	env.cleanOrphansForTest(t)
	ctx := context.Background()
	// 断言失败时也要把容器收掉，否则一次失败就给下一次留下残留。
	defer removeAllLabeled(env.bin, execLabel)

	// 一棵进程树：前台 sleep 撑住 shell，后台 sleep 攥着 stdout ——
	// 这正是「只杀直接子进程」会漏掉的形状。
	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	proc, err := env.iso.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitContainerCount(t, env.bin, execLabel, 1, "容器应当已经起来")

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill: %v", err)
	}

	// 核心断言：容器（连同里面那棵进程树）必须消失。
	waitContainerCount(t, env.bin, execLabel, 0, "Kill 之后容器应当被删除")

	// CLI 侧也要收尾，且不能拖到 WaitDelay 才返回。
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Kill 之后 CLI 进程没有结束")
	}
}

// TestDockerCleanOrphansRemovesExecContainers 模拟「sandboxd 被强杀」留下的
// 一次性容器：下次启动必须能按标签回收。本次改造前它连标签都没有，是隐形的。
func TestDockerCleanOrphansRemovesExecContainers(t *testing.T) {
	env := dockerForTest(t)
	env.cleanOrphansForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 直接起一个带标签的容器，不经过本服务的句柄 —— 等价于上一次运行留下的尸体。
	out, err := exec.CommandContext(ctx, env.bin,
		"run", "-d", "--rm", "--label", execLabel, "--network", "none",
		env.image, "sleep", "300").CombinedOutput()
	if err != nil {
		t.Fatalf("run leftover container: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	defer removeAllLabeled(env.bin, execLabel)
	waitContainerCount(t, env.bin, execLabel, 1, "模拟的残留容器没起来")

	oc := env.iso.(sandbox.OrphanCleaner)
	n, err := oc.CleanOrphans(ctx)
	if err != nil {
		t.Fatalf("clean orphans: %v", err)
	}
	if n == 0 {
		t.Error("CleanOrphans 应当报告回收了容器")
	}
	waitContainerCount(t, env.bin, execLabel, 0, "CleanOrphans 之后不应再有带标签的容器")
}

// TestDockerCliKillLeavesContainerRunning 把「杀 docker CLI ≠ 杀容器」这个前提
// 钉成可执行的事实 —— 它不是在被测行为，而是本次改造的**立论依据**：
// 正因为 CLI 只是通往容器的一根管子，Kill 才必须按名字寻址、走 rm -f。
//
// 一旦本测试开始失败（docker 改了语义），docker 侧的收口方式就该重新审视。
func TestDockerCliKillLeavesContainerRunning(t *testing.T) {
	env := dockerForTest(t)
	env.cleanOrphansForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name := "sandboxd-test-clikill-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cmd := exec.CommandContext(ctx, env.bin,
		"run", "--rm", "-i", "--name", name, "--label", execLabel, "--network", "none",
		env.image, "sleep", "300")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start docker run: %v", err)
	}
	defer func() { _, _ = exec.Command(env.bin, "rm", "-f", name).CombinedOutput() }()

	waitContainerCount(t, env.bin, execLabel, 1, "容器应当已经起来")

	// 旧的做法：只杀掉 CLI 进程本身。
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill cli: %v", err)
	}
	_ = cmd.Wait()

	if n := countContainers(t, env.bin, execLabel); n == 0 {
		t.Error("杀 CLI 之后容器竟然消失了 —— 本测试的前提不再成立，" +
			"请重新审视 docker 为什么需要容器寻址")
	}
}

// countContainers 数带某标签的容器（含已停止的）。
func countContainers(t *testing.T, bin, label string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return len(strings.Fields(string(out)))
}

// waitContainerCount 等容器数量到达期望值（docker 的删除是异步的）。
func waitContainerCount(t *testing.T, bin, label string, want int, msg string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		got = countContainers(t, bin, label)
		if got == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s：带标签 %s 的容器数为 %d，期望 %d", msg, label, got, want)
}

// removeAllLabeled 测试收尾用：把带某标签的容器全删掉（docker rm 只认名字/id，不认 filter）。
func removeAllLabeled(bin, label string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return
	}
	_, _ = exec.Command(bin, append([]string{"rm", "-f"}, ids...)...).CombinedOutput()
}

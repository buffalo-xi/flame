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

// Real-machine acceptance of docker mode's "kill everything": after Kill the container must be gone.
//
// It needs a working docker and image on the machine, otherwise it skips:
//   - SANDBOXD_TEST_DOCKER selects the docker executable (default docker);
//   - SANDBOXD_TEST_IMAGE  selects the image (default flame/sandbox:latest, see sandboxd/Dockerfile).

const defaultTestImage = "flame/sandbox:latest"

// dockerTestEnv holds the three things the real-machine tests need: the isolation implementation, the docker executable
// and the image name.
type dockerTestEnv struct {
	iso   sandbox.Sandbox
	bin   string
	image string
}

// dockerForTest builds a usable docker isolation implementation; it skips when docker / the image is missing.
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
		// The same settings as the docker section of config.yaml, so the test does not exercise a strength that does
		// not exist.
		iso:   NewDocker(Options{Binary: bin, Image: image, Network: "none", ReadOnly: true, User: "1000:1000"}),
		bin:   bin,
		image: image,
	}
}

// cleanOrphansForTest sweeps historical leftovers first: only then does the count mean anything.
func (e dockerTestEnv) cleanOrphansForTest(t *testing.T) {
	t.Helper()
	oc, ok := e.iso.(sandbox.OrphanCleaner)
	if !ok {
		t.Fatal("the docker implementation should implement sandbox.OrphanCleaner")
	}
	n, err := oc.CleanOrphans(context.Background())
	if err != nil {
		t.Fatalf("clean orphans: %v", err)
	}
	if n > 0 {
		t.Logf("collected %d leftover containers before starting", n)
	}
}

// TestDockerKillRemovesContainer: after Kill the container must be gone and the CLI must wrap up too.
func TestDockerKillRemovesContainer(t *testing.T) {
	env := dockerForTest(t)
	env.cleanOrphansForTest(t)
	ctx := context.Background()
	// Clean the container up even when the test fails, otherwise one failure leaves a leftover for the next run.
	defer removeAllLabeled(env.bin, execLabel)

	// A process tree: a foreground sleep keeps the shell alive while a background sleep holds stdout —
	// exactly the shape that "kill only the direct child" would miss.
	spec := &sandbox.Spec{Argv: []string{"/bin/sh", "-c", "sleep 300 & sleep 300"}}
	proc, err := env.iso.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitContainerCount(t, env.bin, execLabel, 1, "the container should be up")

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill: %v", err)
	}

	// The key assertion: the container (and the process tree inside it) must disappear.
	waitContainerCount(t, env.bin, execLabel, 0, "the container should be removed after Kill")

	// The CLI side must wrap up as well, and must not drag on until WaitDelay.
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the CLI process did not end after Kill")
	}
}

// TestDockerCleanOrphansRemovesExecContainers simulates the one-shot containers left behind by a SIGKILLed sandboxd: the
// next start must be able to collect them by label. Before this rework they had no label at all and were invisible.
func TestDockerCleanOrphansRemovesExecContainers(t *testing.T) {
	env := dockerForTest(t)
	env.cleanOrphansForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start a labelled container directly, bypassing this service's handle — equivalent to a corpse from the previous
	// run.
	out, err := exec.CommandContext(ctx, env.bin,
		"run", "-d", "--rm", "--label", execLabel, "--network", "none",
		env.image, "sleep", "300").CombinedOutput()
	if err != nil {
		t.Fatalf("run leftover container: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	defer removeAllLabeled(env.bin, execLabel)
	waitContainerCount(t, env.bin, execLabel, 1, "the simulated leftover container did not start")

	oc := env.iso.(sandbox.OrphanCleaner)
	n, err := oc.CleanOrphans(ctx)
	if err != nil {
		t.Fatalf("clean orphans: %v", err)
	}
	if n == 0 {
		t.Error("CleanOrphans should report that it collected containers")
	}
	waitContainerCount(t, env.bin, execLabel, 0, "no labelled container should remain after CleanOrphans")
}

// TestDockerCliKillLeavesContainerRunning pins "killing the docker CLI ≠ killing the container" as an executable fact —
// this is not the behaviour under test but the **premise** of the rework: precisely because the CLI is merely a pipe into
// the container, Kill has to address the container by name and go through rm -f.
//
// If this test ever starts failing (docker changed the semantics), the docker-side cleanup approach should be
// reconsidered.
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

	waitContainerCount(t, env.bin, execLabel, 1, "the container should be up")

	// The old approach: kill only the CLI process itself.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill cli: %v", err)
	}
	_ = cmd.Wait()

	if n := countContainers(t, env.bin, execLabel); n == 0 {
		t.Error("the container disappeared after killing the CLI — the premise of this test no longer holds, " +
			"please reconsider why docker needs container addressing")
	}
}

// countContainers counts containers carrying a label (stopped ones included).
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

// waitContainerCount waits until the container count reaches the expected value (docker removal is asynchronous).
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
	t.Fatalf("%s: %d containers with label %s, want %d", msg, got, label, want)
}

// removeAllLabeled is the test wrap-up: delete every container with a label (docker rm only takes names/ids, not a
// filter).
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

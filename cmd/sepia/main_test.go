package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The command is the seam between the process and the server, so it is tested in-process where it can be, and
// with a real binary for the one thing that cannot be: a signal.

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"version"}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Error("version printed nothing")
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	for _, args := range [][]string{{"help"}, {}, {"--help"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, nil, &out, &errOut); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(out.String(), "sepia serve") {
			t.Errorf("args %v: usage text missing serve", args)
		}
	}
}

// Bad: an unknown command is a usage error, and says so on stderr rather than pretending to work.
func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"revoke"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// Bad: a config mistake stops the process before it listens, with a message that names the problem.
func TestServeRefusesBadConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"serve"}, []string{"SEPIA_ADDR=0.0.0.0:8893"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "loopback") {
		t.Errorf("stderr = %q, want it to explain the loopback rule", errOut.String())
	}
}

// Sad: the port is already taken, so the process exits non-zero and names the address.
func TestServePortInUse(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := run(ctx, []string{"serve"}, []string{
		"SEPIA_ADDR=" + addr,
		"SEPIA_ALLOWED_HOSTS=" + addr,
		"SEPIA_DATA_DIR=" + t.TempDir(),
	}, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), addr) {
		t.Errorf("stderr = %q, want it to name %s", errOut.String(), addr)
	}
}

// Sad: a cancelled context stops the service and exits 0, which is what a drain should look like.
func TestServeStopsOnContextCancel(t *testing.T) {
	addr := freeAddr(t)
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, []string{
			"SEPIA_ADDR=" + addr,
			"SEPIA_ALLOWED_HOSTS=" + addr,
			"SEPIA_DATA_DIR=" + t.TempDir(),
		}, &out, &errOut)
	}()

	waitHealthy(t, addr, 5*time.Second)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr %s", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not stop after the context was cancelled")
	}
}

// The real binary takes a real SIGTERM and exits 0, which is what Docker will send it.
func TestBinaryHandlesSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildBinary(t)
	addr := freeAddr(t)

	cmd := exec.CommandContext(t.Context(), bin, "serve")
	cmd.Env = append(os.Environ(),
		"SEPIA_ADDR="+addr,
		"SEPIA_ALLOWED_HOSTS="+addr,
		"SEPIA_DATA_DIR="+t.TempDir(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitHealthy(t, addr, 10*time.Second)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v; stderr %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the binary did not exit within the drain deadline")
	}
}

// --- helpers ------------------------------------------------------------------------------------------

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitHealthy polls the health check until it answers, so the test never guesses at a sleep.
func waitHealthy(t *testing.T, addr string, within time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(within)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("healthz = %d", resp.StatusCode)
			}
			var body map[string]string
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if body["status"] != "ok" {
				_ = resp.Body.Close()
				t.Fatalf("healthz body = %v", body)
			}
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never became healthy: %v", lastErr)
}

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sepia")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

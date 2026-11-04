package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The dev stack is the only place the real server and the world meet before CI, so it is booted here once: if
// this test passes, `go run ./cmd/cairn-dev` gives a working offline app.

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestDevStackBootsAndStops(t *testing.T) {
	addr := freeAddr(t)
	var stderr bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	environ := []string{
		"CAIRN_ADDR=" + addr, "CAIRN_ALLOWED_HOSTS=" + addr,
		"CAIRN_DATA_DIR=" + t.TempDir(), "CAIRN_CONFIG_DIR=../../config",
	}
	go func() { done <- run(ctx, environ, &stderr) }()

	// Wait for the port rather than sleeping a guessed amount.
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		reqCtx, reqCancel := context.WithTimeout(context.Background(), time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+addr+"/healthz", nil)
		if err == nil {
			client := &http.Client{Timeout: time.Second}
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
				reqCancel()
				if resp.StatusCode == http.StatusOK {
					lastErr = nil
					break
				}
				lastErr = err
			} else {
				lastErr = err
			}
		}
		reqCancel()
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		cancel()
		t.Fatalf("dev stack never answered: %v; log %s", lastErr, stderr.String())
	}

	if !strings.Contains(stderr.String(), "world ready") {
		t.Errorf("the world was not announced: %s", stderr.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dev stack did not stop")
	}
}

func TestDevStackRefusesBadConfig(t *testing.T) {
	var stderr bytes.Buffer
	err := run(context.Background(), []string{"CAIRN_ADDR=0.0.0.0:8893"}, &stderr)
	if err == nil {
		t.Fatal("run accepted a public address")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error = %v, want it to explain the loopback rule", err)
	}
}

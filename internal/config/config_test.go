package config

import (
	"reflect"
	"strings"
	"testing"
)

// The three routes for the only thing in S00 that reads the outside world: the environment.

func TestLoadDefaults(t *testing.T) {
	got, err := Load(nil)
	if err != nil {
		t.Fatalf("Load(nil): %v", err)
	}
	if got.Addr != "127.0.0.1:8893" {
		t.Errorf("Addr = %q, want the reserved loopback address", got.Addr)
	}
	for _, want := range []string{"localhost:8893", "127.0.0.1:8893", "[::1]:8893", "lepotato.taild95077.ts.net:8893"} {
		if !contains(got.AllowedHosts, want) {
			t.Errorf("AllowedHosts %v is missing %q", got.AllowedHosts, want)
		}
	}
	if got.Dev {
		t.Error("Dev defaulted to true; the zero value must be the safe one")
	}
}

func TestLoadOverrides(t *testing.T) {
	got, err := Load([]string{
		"PATH=/usr/bin",
		"CAIRN_ADDR=127.0.0.1:9000",
		"CAIRN_ALLOWED_HOSTS=localhost:9000, box.lan:9000",
		"CAIRN_DEV=1",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Addr != "127.0.0.1:9000" {
		t.Errorf("Addr = %q", got.Addr)
	}
	if want := []string{"localhost:9000", "box.lan:9000"}; !reflect.DeepEqual(got.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %v, want %v", got.AllowedHosts, want)
	}
	if !got.Dev {
		t.Error("Dev = false, want true")
	}
}

// Bad: a typo in a key must not be ignored, because the operator would then believe a setting took effect.
func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load([]string{"CAIRN_ADDRX=127.0.0.1:8893"})
	if err == nil {
		t.Fatal("Load accepted an unknown CAIRN_ key")
	}
	if !strings.Contains(err.Error(), "CAIRN_ADDRX") {
		t.Errorf("error %q does not name the key", err)
	}
}

// Bad: a value that cannot be used must be refused at load, not at the first request.
func TestLoadRejectsUnusableValues(t *testing.T) {
	cases := map[string][]string{
		"empty addr":        {"CAIRN_ADDR="},
		"no port":           {"CAIRN_ADDR=localhost"},
		"public interface":  {"CAIRN_ADDR=0.0.0.0:8893"},
		"empty host list":   {"CAIRN_ALLOWED_HOSTS="},
		"blank host entry":  {"CAIRN_ALLOWED_HOSTS=localhost:8893, ,box:1"},
		"not a bool":        {"CAIRN_DEV=yes"},
		"host with a path":  {"CAIRN_ALLOWED_HOSTS=http://localhost:8893"},
		"host with a slash": {"CAIRN_ALLOWED_HOSTS=localhost:8893/path"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env); err == nil {
				t.Fatalf("Load(%v) succeeded, want an error", env)
			}
		})
	}
}

// Bad: 0.0.0.0 is refused outright, because the tailnet is the whole security boundary.
func TestLoadRefusesWildcard(t *testing.T) {
	_, err := Load([]string{"CAIRN_ADDR=0.0.0.0:8893"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v, want a refusal that mentions loopback", err)
	}
}

// Bad: duplicate keys are ambiguous, so they are refused rather than last-one-wins.
func TestLoadRejectsDuplicateKey(t *testing.T) {
	_, err := Load([]string{"CAIRN_DEV=0", "CAIRN_DEV=1"})
	if err == nil {
		t.Fatal("Load accepted a duplicate key")
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// The derived origin list and the consistency check are small, but they are the seam between the two header
// checks, so they are tested rather than trusted.

func TestOriginHostsDeriveFromAllowedHosts(t *testing.T) {
	cfg := Config{AllowedHosts: []string{"localhost:8893", "box.lan:9000"}}
	got := cfg.OriginHosts()
	want := []string{
		"http://localhost:8893", "https://localhost:8893",
		"http://box.lan:9000", "https://box.lan:9000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OriginHosts = %v, want %v", got, want)
	}
}

func TestLoadAcceptsLocalhostAsAHostname(t *testing.T) {
	cfg, err := Load([]string{"CAIRN_ADDR=localhost:9001", "CAIRN_ALLOWED_HOSTS=localhost:9001"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "localhost:9001" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
}

func TestLoadRejectsHostsWithoutTheListenPort(t *testing.T) {
	// A health probe would be refused by our own guard, so this is a startup error, not a surprise later.
	_, err := Load([]string{"CAIRN_ADDR=127.0.0.1:9001", "CAIRN_ALLOWED_HOSTS=box.lan:9000"})
	if err == nil || !strings.Contains(err.Error(), "9001") {
		t.Fatalf("err = %v, want a refusal naming the listen port", err)
	}
}

func TestLoadAcceptsHostnamesAndIPv6(t *testing.T) {
	cfg, err := Load([]string{
		"CAIRN_ADDR=[::1]:8893",
		"CAIRN_ALLOWED_HOSTS=[::1]:8893, localhost:8893",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AllowedHosts) != 2 {
		t.Errorf("AllowedHosts = %v", cfg.AllowedHosts)
	}
}

func TestLoadRejectsMalformedEnvironment(t *testing.T) {
	if _, err := Load([]string{"NO_EQUALS_SIGN"}); err == nil {
		t.Fatal("Load accepted a malformed entry")
	}
	// A non-CAIRN_ entry that is malformed is still a malformed entry: the environment is not ours to trust.
	if _, err := Load([]string{"CAIRN_ADDR"}); err == nil {
		t.Fatal("Load accepted a key with no value")
	}
}

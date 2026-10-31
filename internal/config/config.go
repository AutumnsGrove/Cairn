// Package config loads Cairn's settings from the environment.
//
// It is strict on purpose: an unknown CAIRN_ key, a duplicate key, or a value the server cannot use is a
// startup error, not a warning. A setting that silently did nothing would leave the operator believing the
// app was configured the way she asked for (docs/STANDARDS.md 3.7: the zero value is the safe one).
package config

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// DefaultAddr is the address the service listens on. It is loopback only: `tailscale serve` is the only way
// in, and the tailnet is the whole security boundary (docs/STANDARDS.md 6).
const DefaultAddr = "127.0.0.1:8893"

// defaultHosts are the Host values a request may carry. The tailnet name is here because `tailscale serve`
// forwards the browser's Host header unchanged, so a request that reached the service through the tailnet
// still says the tailnet name.
var defaultHosts = []string{
	"localhost:8893",
	"127.0.0.1:8893",
	"[::1]:8893",
	"lepotato.taild95077.ts.net:8893",
}

// Config is everything the server needs to start. Plain data, so a test can build one by hand.
type Config struct {
	// Addr is the listen address, host:port, always loopback.
	Addr string
	// AllowedHosts are the exact Host header values a request may carry, with a port.
	AllowedHosts []string
	// Dev turns on the offline development behaviours: a friendlier error body and the dev port as an
	// allowed Origin. It never relaxes the Host check.
	Dev bool
}

// knownKeys is the closed set of environment keys this package reads. Anything else that starts with
// CAIRN_ is a typo.
var knownKeys = []string{"CAIRN_ADDR", "CAIRN_ALLOWED_HOSTS", "CAIRN_DEV"}

// Load reads settings from environ, in os.Environ() form. Passing the environment in rather than calling
// os.Getenv keeps this pure and lets the tests be table-driven with no global state.
func Load(environ []string) (Config, error) {
	cfg := Config{Addr: DefaultAddr, AllowedHosts: slices.Clone(defaultHosts)}

	seen := make(map[string]bool, len(environ))
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return Config{}, fmt.Errorf("config: malformed environment entry %q", entry)
		}
		if !strings.HasPrefix(key, "CAIRN_") {
			continue
		}
		if seen[key] {
			return Config{}, fmt.Errorf("config: %s is set more than once; that is ambiguous", key)
		}
		seen[key] = true
		values[key] = value
	}

	for key := range values {
		if !slices.Contains(knownKeys, key) {
			return Config{}, fmt.Errorf("config: unknown key %s; known keys are %s", key, strings.Join(knownKeys, ", "))
		}
	}

	if v, ok := values["CAIRN_ADDR"]; ok {
		if err := validateAddr(v); err != nil {
			return Config{}, err
		}
		cfg.Addr = v
	}

	if v, ok := values["CAIRN_ALLOWED_HOSTS"]; ok {
		hosts, err := parseHosts(v)
		if err != nil {
			return Config{}, err
		}
		cfg.AllowedHosts = hosts
	}

	if v, ok := values["CAIRN_DEV"]; ok {
		dev, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: CAIRN_DEV=%q is not a boolean (use 1 or 0)", v)
		}
		cfg.Dev = dev
	}

	// The allowed hosts must cover whatever address we listen on, or every local probe fails.
	if err := cfg.consistent(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateAddr refuses anything that is not loopback. Binding a public interface would put the journal on
// the network the tailnet exists to avoid.
func validateAddr(addr string) error {
	if addr == "" {
		return errors.New("config: CAIRN_ADDR is empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: CAIRN_ADDR=%q is not host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("config: CAIRN_ADDR=%q has no port", addr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("config: CAIRN_ADDR=%q binds every interface; Cairn is loopback only", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if !strings.EqualFold(host, "localhost") {
			return fmt.Errorf("config: CAIRN_ADDR=%q is not an address; Cairn listens on loopback", addr)
		}
		return nil
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("config: CAIRN_ADDR=%q is not loopback; Cairn is loopback only", addr)
	}
	return nil
}

func parseHosts(raw string) ([]string, error) {
	var hosts []string
	for _, part := range strings.Split(raw, ",") {
		h := strings.TrimSpace(part)
		if h == "" {
			return nil, fmt.Errorf("config: CAIRN_ALLOWED_HOSTS has a blank entry in %q", raw)
		}
		if strings.Contains(h, "/") || strings.Contains(h, "://") {
			return nil, fmt.Errorf("config: CAIRN_ALLOWED_HOSTS entry %q must be a bare host:port, not a URL", h)
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			return nil, fmt.Errorf("config: CAIRN_ALLOWED_HOSTS entry %q must include a port: %w", h, err)
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return nil, errors.New("config: CAIRN_ALLOWED_HOSTS is empty; then nothing could reach Cairn")
	}
	return hosts, nil
}

// consistent checks that the listen address is one the Host check would accept, so a local health probe is
// never refused by our own middleware.
func (c Config) consistent() error {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if slices.ContainsFunc(c.AllowedHosts, func(a string) bool {
			return strings.EqualFold(a, net.JoinHostPort(h, port))
		}) {
			return nil
		}
	}
	return fmt.Errorf("config: CAIRN_ALLOWED_HOSTS %v does not include the listen address's port %s on loopback", c.AllowedHosts, port)
}

// OriginHosts returns the origin hosts (scheme://host:port) a state-changing request may come from. They are
// derived from the allowed hosts, so the two checks can never drift apart.
func (c Config) OriginHosts() []string {
	var out []string
	for _, h := range c.AllowedHosts {
		out = append(out, "http://"+h, "https://"+h)
	}
	return out
}

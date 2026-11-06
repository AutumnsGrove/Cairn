// Package config loads Sepia's settings from the environment.
//
// It is strict on purpose: an unknown SEPIA_ key, a duplicate key, or a value the server cannot use is a
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

// DefaultDataDir is where covers and exports live when SEPIA_DATA_DIR is not set. The container mounts this at
// /data in S13; a dev run gets ./data.
const DefaultDataDir = "./data"

// DefaultAddr is the address the service listens on. It is loopback only: `tailscale serve` is the only way
// in, and the tailnet is the whole security boundary (docs/STANDARDS.md 6).
const DefaultAddr = "127.0.0.1:8893"

// DefaultConfigDir is where the shipped question banks and decisions registry live. S13 mounts the same tree.
const DefaultConfigDir = "config"

// DefaultModel is the pinned chat model (docs/BUILD.md 2). The pin is the point: an alias such as -latest or ~
// moves under us, and then every recorded cost and eval measures a different model.
const DefaultModel = "deepseek/deepseek-v4.1-flash-1017"

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
	// TMDBKey enables film and TV metadata. Empty is the normal "not set up" state, never an error: a lookup
	// then answers a typed "off" and the card says so (docs/BUILD.md invariant 13).
	TMDBKey string
	// OpenRouterKey enables the chat model. Empty means the model is off, and the pipeline degrades to a draft
	// she fills in herself rather than failing a turn.
	OpenRouterKey string
	// Model is the pinned chat model id. It is always set; a key is what makes it reachable.
	Model string
	// DataDir holds covers and, later, exports and backups. It is the directory S13 mounts.
	DataDir string
	// ConfigDir holds the question banks and the decisions wording and policy. It is read at startup; a broken
	// file degrades one feature with a logged warning and the service still starts (docs/BUILD.md invariant 13).
	ConfigDir string
}

// knownKeys is the closed set of environment keys this package reads. Anything else that starts with
// SEPIA_ is a typo.
var knownKeys = []string{
	"SEPIA_ADDR", "SEPIA_ALLOWED_HOSTS", "SEPIA_DEV", "SEPIA_DATA_DIR", "SEPIA_CONFIG_DIR",
	"SEPIA_TMDB_API_KEY", "SEPIA_OPENROUTER_API_KEY", "SEPIA_MODEL",
}

// Load reads settings from environ, in os.Environ() form. Passing the environment in rather than calling
// os.Getenv keeps this pure and lets the tests be table-driven with no global state.
func Load(environ []string) (Config, error) {
	cfg := Config{
		Addr: DefaultAddr, AllowedHosts: slices.Clone(defaultHosts), DataDir: DefaultDataDir,
		ConfigDir: DefaultConfigDir, Model: DefaultModel,
	}

	seen := make(map[string]bool, len(environ))
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return Config{}, fmt.Errorf("config: malformed environment entry %q", entry)
		}
		if !strings.HasPrefix(key, "SEPIA_") {
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

	if v, ok := values["SEPIA_ADDR"]; ok {
		if err := validateAddr(v); err != nil {
			return Config{}, err
		}
		cfg.Addr = v
	}

	if v, ok := values["SEPIA_ALLOWED_HOSTS"]; ok {
		hosts, err := parseHosts(v)
		if err != nil {
			return Config{}, err
		}
		cfg.AllowedHosts = hosts
	}

	if v, ok := values["SEPIA_DATA_DIR"]; ok {
		if err := validateDataDir(v); err != nil {
			return Config{}, err
		}
		cfg.DataDir = v
	}

	if v, ok := values["SEPIA_CONFIG_DIR"]; ok {
		if strings.TrimSpace(v) == "" {
			return Config{}, errors.New("config: SEPIA_CONFIG_DIR is empty; omit it to use " + DefaultConfigDir)
		}
		if strings.ContainsRune(v, 0) {
			return Config{}, errors.New("config: SEPIA_CONFIG_DIR contains a NUL byte")
		}
		cfg.ConfigDir = v
	}

	if v, ok := values["SEPIA_TMDB_API_KEY"]; ok {
		if err := validateKey("SEPIA_TMDB_API_KEY", v); err != nil {
			return Config{}, err
		}
		cfg.TMDBKey = v
	}

	if v, ok := values["SEPIA_OPENROUTER_API_KEY"]; ok {
		if err := validateKey("SEPIA_OPENROUTER_API_KEY", v); err != nil {
			return Config{}, err
		}
		cfg.OpenRouterKey = v
	}

	if v, ok := values["SEPIA_MODEL"]; ok {
		if err := validateModel(v); err != nil {
			return Config{}, err
		}
		cfg.Model = v
	}

	if v, ok := values["SEPIA_DEV"]; ok {
		dev, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: SEPIA_DEV=%q is not a boolean (use 1 or 0)", v)
		}
		cfg.Dev = dev
	}

	// The allowed hosts must cover whatever address we listen on, or every local probe fails.
	if err := cfg.consistent(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateDataDir refuses an empty path. A relative path is fine for a dev run; the cover store creates it.
func validateDataDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("config: SEPIA_DATA_DIR is empty; omit it to use " + DefaultDataDir)
	}
	if strings.ContainsRune(dir, 0) {
		return errors.New("config: SEPIA_DATA_DIR contains a NUL byte")
	}
	return nil
}

// validateKey refuses a key with whitespace in it, which is almost always a key pasted with a newline. The
// message names the variable and never repeats the key.
func validateKey(name, key string) error {
	if strings.TrimSpace(key) != key {
		return fmt.Errorf("config: %s has leading or trailing space", name)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("config: %s contains whitespace; it was probably pasted with a newline", name)
	}
	return nil
}

// validateModel refuses an empty id, a role name (no slash) and the two alias spellings nova refuses. The main
// model is pinned, so a moving id is a startup error rather than a quiet change of what the journal runs on.
func validateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("config: SEPIA_MODEL is empty; omit it to use " + DefaultModel)
	}
	if model != strings.TrimSpace(model) || strings.ContainsAny(model, " \t\r\n") {
		return errors.New("config: SEPIA_MODEL contains whitespace")
	}
	if strings.HasSuffix(model, "-latest") || strings.HasPrefix(model, "~") {
		return fmt.Errorf("config: SEPIA_MODEL=%q is an alias; name a pinned model id", model)
	}
	if !strings.Contains(model, "/") {
		return fmt.Errorf("config: SEPIA_MODEL=%q is not a provider/model id", model)
	}
	return nil
}

// validateAddr refuses anything that is not loopback. Binding a public interface would put the journal on
// the network the tailnet exists to avoid.
func validateAddr(addr string) error {
	if addr == "" {
		return errors.New("config: SEPIA_ADDR is empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: SEPIA_ADDR=%q is not host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("config: SEPIA_ADDR=%q has no port", addr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("config: SEPIA_ADDR=%q binds every interface; Sepia is loopback only", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if !strings.EqualFold(host, "localhost") {
			return fmt.Errorf("config: SEPIA_ADDR=%q is not an address; Sepia listens on loopback", addr)
		}
		return nil
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("config: SEPIA_ADDR=%q is not loopback; Sepia is loopback only", addr)
	}
	return nil
}

func parseHosts(raw string) ([]string, error) {
	var hosts []string
	for _, part := range strings.Split(raw, ",") {
		h := strings.TrimSpace(part)
		if h == "" {
			return nil, fmt.Errorf("config: SEPIA_ALLOWED_HOSTS has a blank entry in %q", raw)
		}
		if strings.Contains(h, "/") || strings.Contains(h, "://") {
			return nil, fmt.Errorf("config: SEPIA_ALLOWED_HOSTS entry %q must be a bare host:port, not a URL", h)
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			return nil, fmt.Errorf("config: SEPIA_ALLOWED_HOSTS entry %q must include a port: %w", h, err)
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return nil, errors.New("config: SEPIA_ALLOWED_HOSTS is empty; then nothing could reach Sepia")
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
	return fmt.Errorf("config: SEPIA_ALLOWED_HOSTS %v does not include the listen address's port %s on loopback", c.AllowedHosts, port)
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

// Package archtest holds the build-time check that Cairn's dependency rule holds.
//
// The rule is in docs/BUILD.md section 4: domain is pure, the adapters import it and nothing else of ours,
// the layers above may reach only downwards, world is a test double, and nova is named in one place. The
// check reads the real import graph with `go list`, so a package cannot pass by claiming to be somewhere
// else, and it is a normal `go test` failure, so CI already runs it.
//
// Adding a package under internal/ means adding it to the allow table below. That is on purpose: a new
// package should have to say which layer it belongs to out loud.
package archtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// module is the module path, which is also the prefix of every import of our own code.
const module = "github.com/AutumnsGrove/cairn"

// novaModule is the only outside module our rules name.
const novaModule = "github.com/AutumnsGrove/nova"

// novaPackages lists, per layer, the nova subtrees that layer may import. A layer that is absent may not
// import nova at all. nova's agent shell (mode, config, session, tui, bundled) is never importable from
// Cairn: only internal/agent takes the embedding, internal/decisions adapts the decide contract, and
// internal/world fakes that contract.
var novaPackages = map[string][]string{
	"agent":     nil, // the embedding: any nova package
	"decisions": {"decide", "provider"},
	"world":     {"decide", "provider"},
}

// allow lists, per layer under internal/, the other layers that layer may import. A layer may always import
// its own subpackages. domain imports nothing; the adapters import domain only; the layers above reach down.
var allow = map[string][]string{
	"domain":    {},
	"version":   {},
	"config":    {},
	"banks":     {"domain"},
	"decisions": {"domain"},
	"store":     {"domain"},
	"providers": {"domain"},
	"session":   {"domain", "store", "banks"},
	"agent":     {"domain", "store", "banks", "session", "decisions", "providers"},
	"server":    {"domain", "session", "agent", "update", "config", "webui", "version"},
	"update":    {"domain", "config"},
	"backup":    {"domain", "store", "config"},
	"webui":     {},
	"world":     {"domain"},
}

// pkg is the slice of `go list -json` this test needs.
type pkg struct {
	ImportPath   string
	Dir          string
	Name         string
	Imports      []string
	TestImports  []string
	XTestImports []string
	GoFiles      []string
	CgoFiles     []string
}

func TestDependencyRule(t *testing.T) {
	pkgs := list(t)

	got := check(pkgs)
	if len(got) == 0 {
		return
	}
	for _, v := range got {
		t.Errorf("dependency rule: %s", v)
	}
	t.Fatalf("%d violation(s) of docs/BUILD.md section 4", len(got))
}

// check returns one human-readable line per violation.
func check(pkgs []pkg) []string {
	var out []string
	for _, p := range pkgs {
		if !isOurs(p.ImportPath) {
			continue
		}
		self := layer(p.ImportPath)

		for _, imp := range p.Imports {
			if v := violation(p.ImportPath, self, imp); v != "" {
				out = append(out, v)
			}
		}

		// world is a test double: it may appear in a test file (which `go list` reports separately, in
		// TestImports) or in cmd/cairn-dev, never in shipped code.
		if !isCmdDev(p.ImportPath) {
			for _, imp := range p.Imports {
				if imp == module+"/internal/world" {
					out = append(out, fmt.Sprintf("%s imports internal/world outside a test; the world is a fake", p.ImportPath))
				}
			}
		}

		// A provider endpoint belongs to its client. server, agent and world know the interface, not the URL.
		if !strings.HasPrefix(p.ImportPath, module+"/internal/providers") && !strings.HasPrefix(p.ImportPath, module+"/internal/world") {
			out = append(out, endpointLiterals(p)...)
		}
	}
	return out
}

func violation(self, selfLayer, imp string) string {
	// A command is the composition root: it wires concrete packages together and is allowed to know them.
	if strings.HasPrefix(selfLayer, "cmd/") {
		return ""
	}

	// A provider's own subpackages (providers/tmdb and so on) are reached only through providers, so a swap
	// touches one package. The world fakes them, so it may too; a test may, because go list reports test
	// imports separately and they are not checked here.
	for _, family := range []string{"tmdb", "openlibrary", "polaris"} {
		if imp != module+"/internal/providers/"+family {
			continue
		}
		if selfLayer == "providers" || selfLayer == "world" {
			return ""
		}
		return fmt.Sprintf("%s imports %s directly; go through internal/providers so the client stays swappable", self, imp)
	}

	if strings.HasPrefix(imp, novaModule) {
		allowed, ok := novaPackages[selfLayer]
		if !ok {
			return fmt.Sprintf("%s imports nova (%s); only internal/agent, internal/decisions and internal/world may name nova", self, imp)
		}
		if allowed == nil {
			return ""
		}
		sub := strings.TrimPrefix(strings.TrimPrefix(imp, novaModule), "/")
		for _, want := range allowed {
			if sub == want || strings.HasPrefix(sub, want+"/") {
				return ""
			}
		}
		return fmt.Sprintf("%s imports nova's %q; %s may import nova only from %s", self, sub, selfLayer, strings.Join(allowed, ", "))
	}

	if imp == module+"/internal" || strings.HasPrefix(imp, module+"/internal/") {
		targetLayer := layer(imp)
		if targetLayer == selfLayer {
			return ""
		}
		allowed, known := allow[selfLayer]
		if !known {
			return fmt.Sprintf("%s is under internal/ but is not in the allow table; add its layer to internal/archtest", self)
		}
		if len(allowed) == 0 {
			return fmt.Sprintf("%s (%s) imports %s, but %s imports no internal package", self, selfLayer, imp, selfLayer)
		}
		if slices.Contains(allowed, targetLayer) {
			return ""
		}
		return fmt.Sprintf("%s (%s) imports %s; %s may import only %s", self, selfLayer, imp, selfLayer, strings.Join(allowed, ", "))
	}
	return ""
}

var endpointRE = regexp.MustCompile(`https://(api\.themoviedb\.org|openlibrary\.org|openrouter\.ai|systemone)`)

// endpointLiterals reports a hard-coded provider endpoint outside the package that owns it.
func endpointLiterals(p pkg) []string {
	var out []string
	for _, name := range p.GoFiles {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			continue
		}
		if m := endpointRE.Find(b); m != nil {
			out = append(out, fmt.Sprintf("%s names the endpoint %q in %s; endpoints live in internal/providers", p.ImportPath, m, name))
		}
	}
	return out
}

func list(t *testing.T) []pkg {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.CommandContext(context.Background(), "go", "list", "-e", "-json", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		if p.ImportPath == "" || len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue // a directory with no buildable Go files, such as the embed placeholder
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Fatal("not inside a Go module")
	}
	return filepath.Dir(mod)
}

func isOurs(importPath string) bool {
	return importPath == module || strings.HasPrefix(importPath, module+"/")
}

// layer is the first path element under internal/, or the whole command path for cmd/.
func layer(importPath string) string {
	rest := strings.TrimPrefix(importPath, module+"/")
	if after, ok := strings.CutPrefix(rest, "internal/"); ok {
		return strings.SplitN(after, "/", 2)[0]
	}
	return rest
}

func isCmdDev(importPath string) bool {
	return importPath == module+"/cmd/cairn-dev"
}

// TestCheckRejects proves the checker has teeth, on synthetic graphs, so a broken checker cannot make the
// real test pass by finding nothing.
func TestCheckRejects(t *testing.T) {
	cases := []struct {
		name string
		pkgs []pkg
		want string
	}{
		{
			name: "domain imports store",
			pkgs: []pkg{{
				ImportPath: module + "/internal/domain",
				Dir:        t.TempDir(),
				Imports:    []string{module + "/internal/store"},
			}},
			want: "imports no internal package",
		},
		{
			name: "store imports session",
			pkgs: []pkg{{
				ImportPath: module + "/internal/store",
				Dir:        t.TempDir(),
				Imports:    []string{module + "/internal/session"},
			}},
			want: "store may import only domain",
		},
		{
			name: "banks imports agent",
			pkgs: []pkg{{
				ImportPath: module + "/internal/banks",
				Dir:        t.TempDir(),
				Imports:    []string{module + "/internal/agent"},
			}},
			want: "may import only domain",
		},
		{
			name: "server reaches into a provider",
			pkgs: []pkg{{
				ImportPath: module + "/internal/server",
				Dir:        t.TempDir(),
				Imports:    []string{module + "/internal/providers/tmdb"},
			}},
			want: "go through internal/providers",
		},
		{
			name: "session imports nova",
			pkgs: []pkg{{
				ImportPath: module + "/internal/session",
				Dir:        t.TempDir(),
				Imports:    []string{novaModule + "/agent"},
			}},
			want: "only internal/agent, internal/decisions and internal/world",
		},
		{
			name: "decisions imports nova's app shell",
			pkgs: []pkg{{
				ImportPath: module + "/internal/decisions",
				Dir:        t.TempDir(),
				Imports:    []string{novaModule + "/agent"},
			}},
			want: "may import nova only from decide, provider",
		},
		{
			name: "banks imports nova's decide",
			pkgs: []pkg{{
				ImportPath: module + "/internal/banks",
				Dir:        t.TempDir(),
				Imports:    []string{novaModule + "/decide"},
			}},
			want: "only internal/agent, internal/decisions and internal/world",
		},
		{
			name: "shipped code imports the world",
			pkgs: []pkg{{
				ImportPath: module + "/internal/server",
				Dir:        t.TempDir(),
				GoFiles:    []string{"server.go"},
				Imports:    []string{module + "/internal/world"},
			}},
			want: "the world is a fake",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := check(tc.pkgs)
			if len(got) == 0 {
				t.Fatalf("checker found nothing, want a violation mentioning %q", tc.want)
			}
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("checker said:\n%s\nwant it to mention %q", joined, tc.want)
			}
		})
	}
}

// TestCheckAllows proves the rule is not so strict that legitimate code cannot exist, so the real test does
// not get "fixed" by widening the allow table.
func TestCheckAllows(t *testing.T) {
	ok := []pkg{
		{ImportPath: module + "/internal/domain", Dir: t.TempDir()},
		{ImportPath: module + "/internal/version", Dir: t.TempDir()},
		{ImportPath: module + "/internal/store", Dir: t.TempDir(), Imports: []string{module + "/internal/domain"}},
		{ImportPath: module + "/internal/session", Dir: t.TempDir(), Imports: []string{module + "/internal/domain", module + "/internal/store", module + "/internal/banks"}},
		{ImportPath: module + "/internal/agent", Dir: t.TempDir(), Imports: []string{
			module + "/internal/session", module + "/internal/decisions", module + "/internal/providers", novaModule + "/agent", "net/http",
		}},
		{ImportPath: module + "/internal/decisions", Dir: t.TempDir(), Imports: []string{novaModule + "/decide"}},
		{ImportPath: module + "/internal/decisions/pre-read", Dir: t.TempDir(), Imports: []string{novaModule + "/decide"}},
		{ImportPath: module + "/internal/providers/tmdb", Dir: t.TempDir(), Imports: []string{module + "/internal/domain"}},
		{ImportPath: module + "/internal/world", Dir: t.TempDir(), Imports: []string{module + "/internal/domain", module + "/internal/providers/tmdb"}},
		{ImportPath: module + "/cmd/cairn-dev", Dir: t.TempDir(), GoFiles: []string{"main.go"}, Imports: []string{module + "/internal/world"}},
	}
	if got := check(ok); len(got) != 0 {
		t.Fatalf("checker rejected legitimate code:\n%s", strings.Join(got, "\n"))
	}
}

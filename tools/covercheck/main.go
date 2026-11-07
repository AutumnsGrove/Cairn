// Command covercheck fails when a package's statement coverage is below its floor.
//
// The CI job used to average the per-function percentages `go tool cover -func` prints, which weights every
// function equally and reports a number that is not the one the docs promise. This reads the profile itself and
// counts statements, which is what `go test` reports and what docs/CI.md means by "coverage".
//
// The floors live here rather than in the workflow so there is one place to change, a reason to record and a unit
// test that proves the arithmetic.
//
//	go run ./tools/covercheck -profile=/tmp/cover.out
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// module is the import path prefix every profile entry starts with.
const module = "github.com/AutumnsGrove/sepia/"

// generated holds the packages that are generated rather than written, so they are excluded from the count of the
// package above them. sqlc's package has no tests by design and is covered through the repositories.
var generated = []string{"internal/store/db"}

// floors are the critical packages and their statement-coverage floors, highest bar first.
//
// Lowering a floor is a last resort: docs/CI.md 1 records the reason and the date beside it.
var floors = []floor{
	{pkg: "internal/domain", percent: 95},
	{pkg: "internal/decisions", percent: 88},
	{pkg: "internal/banks", percent: 85},
	{pkg: "internal/providers", percent: 85},
	{pkg: "internal/store", percent: 84},
	{pkg: "internal/server", percent: 80},
	{pkg: "internal/config", percent: 80},
	{pkg: "internal/world", percent: 80},
}

// floor is one package's bar.
type floor struct {
	pkg     string
	percent int
}

// result is what one package scored against its floor.
type result struct {
	floor
	covered int
	total   int
}

// ok reports whether the package met its floor. The comparison is done in statements rather than in a rounded
// percentage, so 95.4% clears a floor of 95 and 94.9% does not.
func (r result) ok() bool { return r.covered*100 >= r.percent*r.total }

// String renders one line of the report.
func (r result) String() string {
	return fmt.Sprintf("%s coverage: %.1f%% (floor %d%%)", r.pkg, 100*float64(r.covered)/float64(r.total), r.percent)
}

func main() {
	profile := flag.String("profile", "cover.out", "the coverage profile written by go test -coverprofile")
	flag.Parse()
	if err := run(*profile, os.Stdout, floors); err != nil {
		fmt.Fprintln(os.Stderr, "covercheck:", err)
		os.Exit(1)
	}
}

// run reads the profile, prints one line per package and returns an error naming every package below its floor.
func run(path string, out io.Writer, floors []floor) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open profile: %w", err)
	}
	defer func() { _ = f.Close() }()
	results, err := check(f, floors)
	if err != nil {
		return err
	}
	var failed []string
	for _, r := range results {
		fmt.Fprintln(out, r)
		if !r.ok() {
			failed = append(failed, fmt.Sprintf("%s is %.1f%%, below the %d%% floor",
				r.pkg, 100*float64(r.covered)/float64(r.total), r.percent))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// check sums the statements of each floor's package. A package with no profile entry at all is an error rather than
// a zero, because that means its tests did not run and the job would otherwise pass on a lie.
func check(profile io.Reader, want []floor) ([]result, error) {
	covered := map[string]int{}
	total := map[string]int{}
	blocks, err := scan(profile, covered, total)
	if err != nil {
		return nil, err
	}
	if blocks == 0 {
		return nil, errors.New("the profile is empty: no tests ran, or no package was instrumented")
	}
	out := make([]result, 0, len(want))
	for _, f := range want {
		if total[f.pkg] == 0 {
			return nil, fmt.Errorf("no coverage for %s: the profile has no statements for it", f.pkg)
		}
		out = append(out, result{floor: f, covered: covered[f.pkg], total: total[f.pkg]})
	}
	return out, nil
}

// scan reads a coverage profile in its documented format:
//
//	module/pkg/file.go:startLine.startCol,endLine.endCol statements count
//
// and adds each block to its package. Generated packages are skipped, so the hand-written package beside them is not
// judged on code nobody wrote.
func scan(profile io.Reader, covered, total map[string]int) (int, error) {
	scanner := bufio.NewScanner(profile)
	blocks := 0
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "mode:") {
			continue
		}
		pkg, stmts, count, err := parseBlock(text)
		if err != nil {
			return 0, fmt.Errorf("line %d: %w", line, err)
		}
		if isGenerated(pkg) {
			continue
		}
		total[pkg] += stmts
		if count > 0 {
			covered[pkg] += stmts
		}
		blocks++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read profile: %w", err)
	}
	return blocks, nil
}

// parseBlock splits one profile line into its package, its statement count and how many times it ran.
func parseBlock(text string) (pkg string, stmts, count int, err error) {
	fields := strings.Fields(text)
	if len(fields) != 3 {
		return "", 0, 0, fmt.Errorf("not a coverage block: %q", text)
	}
	colon := strings.LastIndex(fields[0], ":")
	if colon < 0 {
		return "", 0, 0, fmt.Errorf("no position in %q", fields[0])
	}
	file := fields[0][:colon]
	rel, ok := strings.CutPrefix(file, module)
	if !ok {
		return "", 0, 0, fmt.Errorf("%q is not a file in this module", file)
	}
	slash := strings.LastIndex(rel, "/")
	if slash < 0 {
		return "", 0, 0, fmt.Errorf("%q is not in a package directory", file)
	}
	stmts, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("statement count in %q: %w", text, err)
	}
	count, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, 0, fmt.Errorf("run count in %q: %w", text, err)
	}
	if stmts < 0 || count < 0 {
		return "", 0, 0, fmt.Errorf("negative count in %q", text)
	}
	return rel[:slash], stmts, count, nil
}

// isGenerated reports whether a package is generated rather than written by hand.
func isGenerated(pkg string) bool {
	for _, gen := range generated {
		if pkg == gen || strings.HasPrefix(pkg, gen+"/") {
			return true
		}
	}
	return false
}

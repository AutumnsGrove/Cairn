package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool decides whether main is green, so its arithmetic is tested on the three routes: a profile that clears its
// floors, one that misses, and one that is not a profile at all.

const header = "mode: set\n"

func TestCheckReportsStatementCoveragePerPackage(t *testing.T) {
	// A package with one covered block of three statements and one missed block of one: 75%, not the 50% a count of
	// blocks would give and not the 0% averaging the func lines would give.
	profile := header +
		"github.com/AutumnsGrove/sepia/internal/domain/a.go:1.1,2.2 3 1\n" +
		"github.com/AutumnsGrove/sepia/internal/domain/b.go:5.1,6.2 1 0\n"
	results, err := check(strings.NewReader(profile), []floor{{pkg: "internal/domain", percent: 75}})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].covered != 3 || results[0].total != 4 {
		t.Fatalf("covered %d of %d, want 3 of 4", results[0].covered, results[0].total)
	}
	if !results[0].ok() {
		t.Errorf("%v did not clear its floor", results[0])
	}
	if got, want := results[0].String(), "internal/domain coverage: 75.0% (floor 75%)"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

func TestCheckFailsAPackageUnderItsFloor(t *testing.T) {
	profile := header +
		"github.com/AutumnsGrove/sepia/internal/server/a.go:1.1,2.2 1 1\n" +
		"github.com/AutumnsGrove/sepia/internal/server/b.go:5.1,6.2 9 0\n"
	results, err := check(strings.NewReader(profile), []floor{{pkg: "internal/server", percent: 20}})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if results[0].ok() {
		t.Fatalf("%v passed a floor of 20%% on 10%%", results[0])
	}

	// The failure names the package and both numbers, so a log reader does not have to work it out.
	var out strings.Builder
	profilePath := writeProfile(t, profile)
	if err := run(profilePath, &out, []floor{{pkg: "internal/server", percent: 20}}); err == nil {
		t.Fatal("run passed a profile below its floors")
	} else if !strings.Contains(err.Error(), "internal/server") {
		t.Errorf("error does not name the package: %v", err)
	}
	if !strings.Contains(out.String(), "coverage:") {
		t.Errorf("the report is empty: %q", out.String())
	}
}

func TestCheckComparesInStatementsNotRoundedPercent(t *testing.T) {
	// 95.4% of statements clears a floor of 95 even though a rounded "94%" once failed it, and 94.9% does not.
	high := header + "github.com/AutumnsGrove/sepia/internal/domain/a.go:1.1,2.2 954 1\n" +
		"github.com/AutumnsGrove/sepia/internal/domain/b.go:3.1,4.2 46 0\n"
	results, err := check(strings.NewReader(high), []floor{{pkg: "internal/domain", percent: 95}})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !results[0].ok() {
		t.Errorf("95.4%% did not clear a floor of 95: %v", results[0])
	}
	low := header + "github.com/AutumnsGrove/sepia/internal/domain/a.go:1.1,2.2 949 1\n" +
		"github.com/AutumnsGrove/sepia/internal/domain/b.go:3.1,4.2 51 0\n"
	results, err = check(strings.NewReader(low), []floor{{pkg: "internal/domain", percent: 95}})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if results[0].ok() {
		t.Errorf("94.9%% cleared a floor of 95")
	}
}

func TestGeneratedCodeIsNotCounted(t *testing.T) {
	profile := header +
		"github.com/AutumnsGrove/sepia/internal/store/store.go:1.1,2.2 2 1\n" +
		"github.com/AutumnsGrove/sepia/internal/store/db/queries.sql.go:1.1,2.2 100 0\n"
	results, err := check(strings.NewReader(profile), []floor{{pkg: "internal/store", percent: 100}})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if results[0].total != 2 || !results[0].ok() {
		t.Fatalf("generated code was counted: %+v", results[0])
	}
}

func TestCheckRefusesAProfileItCannotTrust(t *testing.T) {
	cases := map[string]string{
		"empty":            header,
		"no such package":  header + "github.com/AutumnsGrove/sepia/internal/banks/a.go:1.1,2.2 2 1\n",
		"not a block":      header + "nonsense\n",
		"missing position": header + "github.com/AutumnsGrove/sepia/internal/domain 2 1\n",
		"not this module":  header + "example.test/other/a.go:1.1,2.2 2 1\n",
		"bad count":        header + "github.com/AutumnsGrove/sepia/internal/domain/a.go:1.1,2.2 two 1\n",
	}
	floors := []floor{{pkg: "internal/domain", percent: 50}}
	for name, profile := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := check(strings.NewReader(profile), floors); err == nil {
				t.Fatal("a profile that cannot be trusted was accepted")
			}
		})
	}
	if err := run("/nonexistent/cover.out", &strings.Builder{}, floors); err == nil {
		t.Error("a missing profile was accepted")
	}
}

// writeProfile puts a profile on disk for the tests that drive run itself.
func writeProfile(t *testing.T, profile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

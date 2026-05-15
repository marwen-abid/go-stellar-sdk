package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIntegration_GenerateMatchesGolden is the end-to-end shake-out for the
// T8.1–T8.3 pipeline (T8.4). It builds the sorobangen binary, runs it against
// a committed spec fixture, and asserts the output is byte-for-byte identical
// to the committed golden file. On mismatch the test points the user at the
// regeneration command so the fix is one keystroke away.
//
// The companion //go:generate directive below lets users refresh the golden
// in place by running `go generate ./cmd/sorobangen/...` from the repo root.
// The test itself does NOT depend on the directive having been run — it runs
// the generator from scratch on every invocation.
//
//go:generate go run . -spec testdata/integration/input.spec.bin -out testdata/integration/gen -package gen

func TestIntegration_GenerateMatchesGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sorobangen integration test in -short mode")
	}

	repoRoot := findRepoRoot(t)
	fixture := filepath.Join(repoRoot, "cmd", "sorobangen", "testdata", "integration", "input.spec.bin")
	goldenPath := filepath.Join(repoRoot, "cmd", "sorobangen", "testdata", "integration", "gen", "gen.go")

	if _, err := os.Stat(fixture); err != nil {
		t.Fatalf("fixture missing at %s: %v", fixture, err)
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}

	// Build the sorobangen binary into a scratch dir. `go run` would work but
	// pinning the binary makes the failure mode (build vs. emit) easier to
	// diagnose if this test ever regresses.
	scratch := t.TempDir()
	binPath := filepath.Join(scratch, "sorobangen")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/stellar/go-stellar-sdk/cmd/sorobangen")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build sorobangen: %v\n%s", err, out)
	}

	// Run the generator against the fixture into a fresh output dir.
	outDir := filepath.Join(scratch, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	genCmd := exec.Command(binPath, "-spec", fixture, "-out", outDir, "-package", "gen")
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("run sorobangen: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "gen.go"))
	if err != nil {
		t.Fatalf("read generated output: %v", err)
	}

	if !bytes.Equal(got, golden) {
		t.Errorf("generated source does not match golden %s.\n"+
			"Fresh output written to %s.\n"+
			"To refresh: go generate ./cmd/sorobangen/...",
			goldenPath, filepath.Join(outDir, "gen.go"))
	}

	// Parse the freshly-generated source as a sanity gate beyond the byte
	// comparison: a stricter shake-out (full `go build` of the emitted
	// package) is T8.5's job, where the SAC binding is exercised against the
	// real contract package. Here we just confirm the output is syntactically
	// valid Go so a malformed emitter regression is visible from this test
	// alone.
	if _, err := parser.ParseFile(token.NewFileSet(), "gen.go", got, parser.ParseComments); err != nil {
		t.Errorf("generated source does not parse: %v\n--- source ---\n%s", err, got)
	}
}

// TestSACBindingSmoke closes Phase 8: it proves the codegen pipeline produces
// working Go on a realistic input — the bundled SAC spec, which carries
// 16 functions, 8 events, addresses, i128s, and a handful of bool/string
// return types. The check is broader than T8.4's golden-diff because it also
// `go build`s and `go test`s the emitted package, exercising the generated
// (*Client).Balance / .Decimals / .Symbol against a fake rpc.
//
// Steps:
//  1. Regenerate the binding from asset/sac_spec.bin into a scratch dir.
//  2. Diff the regenerated file against the committed golden under
//     cmd/sorobangen/testdata/sac/gen/sac.go.
//  3. `go build` the committed package directly so a stale golden surfaces as
//     a build failure rather than a silent diff.
//  4. `go test` the committed package's in-tree smoke test (sac_test.go),
//     which calls the generated Client against an in-memory rpc.
//
//go:generate go run . -spec ../../asset/sac_spec.bin -out testdata/sac/gen -package sac -contract-id CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA
func TestSACBindingSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SAC binding smoke test in -short mode")
	}

	repoRoot := findRepoRoot(t)
	specPath := filepath.Join(repoRoot, "asset", "sac_spec.bin")
	goldenPath := filepath.Join(repoRoot, "cmd", "sorobangen", "testdata", "sac", "gen", "sac.go")
	const contractID = "CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA"

	if _, err := os.Stat(specPath); err != nil {
		t.Fatalf("SAC spec missing at %s: %v", specPath, err)
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}

	scratch := t.TempDir()
	binPath := filepath.Join(scratch, "sorobangen")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/stellar/go-stellar-sdk/cmd/sorobangen")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build sorobangen: %v\n%s", err, out)
	}

	outDir := filepath.Join(scratch, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	genCmd := exec.Command(binPath,
		"-spec", specPath,
		"-out", outDir,
		"-package", "sac",
		"-contract-id", contractID,
	)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("run sorobangen against SAC spec: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "sac.go"))
	if err != nil {
		t.Fatalf("read regenerated SAC binding: %v", err)
	}
	if !bytes.Equal(got, golden) {
		t.Errorf("regenerated SAC binding does not match golden %s.\n"+
			"Fresh output written to %s.\n"+
			"To refresh: go generate ./cmd/sorobangen/...",
			goldenPath, filepath.Join(outDir, "sac.go"))
	}

	// Confirm the committed golden compiles. The integration test in T8.4
	// stopped at a syntactic parse check; T8.5 takes the next step so a stale
	// import / signature mismatch in the emitter is caught here rather than
	// only at downstream consumption.
	pkgPath := "github.com/stellar/go-stellar-sdk/cmd/sorobangen/testdata/sac/gen"
	cb := exec.Command("go", "build", pkgPath)
	cb.Dir = repoRoot
	if out, err := cb.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkgPath, err, out)
	}

	// Run the in-package smoke test that exercises (*Client).Balance /
	// .Decimals / .Symbol against an in-memory rpc. This is what proves the
	// generated Spec actually decodes a SAC happy-path response.
	ct := exec.Command("go", "test", "-count=1", pkgPath)
	ct.Dir = repoRoot
	if out, err := ct.CombinedOutput(); err != nil {
		t.Fatalf("go test %s: %v\n%s", pkgPath, err, out)
	}
}

// findRepoRoot walks up from this test file's directory until it finds a
// go.mod with the SDK module path. Test working directories vary between
// `go test` invocations and CI; this anchoring keeps the test robust.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		modPath := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modPath); err == nil {
			if bytes.Contains(data, []byte("module github.com/stellar/go-stellar-sdk")) {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate SDK go.mod from %s", wd)
		}
		dir = parent
	}
}

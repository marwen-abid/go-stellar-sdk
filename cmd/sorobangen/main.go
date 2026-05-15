// sorobangen is the Go counterpart of the stellar-js `stellar-js generate`
// command. It ingests a Soroban contract's spec — either embedded in a
// WebAssembly binary (`-wasm`) or as a raw XDR ScSpecEntry stream
// (`-spec`) — and writes a typed Go binding package.
//
// T8.1 wires up flag parsing, spec extraction, and output-directory
// preparation; it emits only a placeholder source file. T8.2 will replace
// the placeholder with `text/template`-driven emission of client.go,
// methods.go, types.go, errors.go, events.go, and spec.go.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// options collects the parsed command-line flags. Kept as a struct so
// run() can be exercised directly from tests without touching the global
// flag set.
type options struct {
	wasmPath    string
	specPath    string
	outDir      string
	packageName string
	contractID  string
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		// flag.ErrHelp is returned when the user passes -h/-help; the flag
		// package has already printed usage, so we just exit cleanly.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "sorobangen: %v\n", err)
		os.Exit(1)
	}
}

// run parses flags from args and performs the generation. It is the
// testable entry point used by the unit tests.
func run(args []string, errOut io.Writer) error {
	fs := flag.NewFlagSet("sorobangen", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "Usage: sorobangen [flags]")
		fmt.Fprintln(errOut, "  Generate a Go binding package for a Soroban contract.")
		fmt.Fprintln(errOut, "  Exactly one of -wasm or -spec is required.")
		fs.PrintDefaults()
	}

	var opts options
	fs.StringVar(&opts.wasmPath, "wasm", "", "path to a contract WASM file containing a 'contractspecv0' custom section")
	fs.StringVar(&opts.specPath, "spec", "", "path to a raw XDR ScSpecEntry stream (alternative to -wasm)")
	fs.StringVar(&opts.outDir, "out", "", "output directory for the generated package (required)")
	fs.StringVar(&opts.packageName, "package", "", "Go package name for the generated code (required)")
	fs.StringVar(&opts.contractID, "contract-id", "", "deployed contract strkey (C...); when set, the generated init() registers the spec under this id via contract.RegisterSpec")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := opts.validate(); err != nil {
		fs.Usage()
		return err
	}

	spec, err := loadSpec(opts)
	if err != nil {
		return err
	}

	if err := emit(opts.outDir, opts.packageName, opts.contractID, spec); err != nil {
		return fmt.Errorf("emitting bindings: %w", err)
	}
	return nil
}

func (o options) validate() error {
	switch {
	case o.wasmPath == "" && o.specPath == "":
		return errors.New("one of -wasm or -spec must be set")
	case o.wasmPath != "" && o.specPath != "":
		return errors.New("-wasm and -spec are mutually exclusive")
	case o.outDir == "":
		return errors.New("-out is required")
	case o.packageName == "":
		return errors.New("-package is required")
	}
	if o.contractID != "" && !strkey.IsValidContractAddress(o.contractID) {
		return fmt.Errorf("-contract-id %q is not a valid contract strkey (must start with 'C')", o.contractID)
	}
	return nil
}

// loadSpec reads the spec bytes from the chosen input source and returns a
// parsed *contract.Spec. The CLI delegates the WASM custom-section walk
// and XDR decoding to the contract package so the two callers stay in
// lockstep.
func loadSpec(o options) (*contract.Spec, error) {
	if o.wasmPath != "" {
		raw, err := os.ReadFile(o.wasmPath)
		if err != nil {
			return nil, fmt.Errorf("reading wasm: %w", err)
		}
		spec, err := contract.NewSpecFromWasm(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing wasm spec: %w", err)
		}
		return spec, nil
	}

	raw, err := os.ReadFile(o.specPath)
	if err != nil {
		return nil, fmt.Errorf("reading spec: %w", err)
	}
	spec, err := contract.NewSpecFromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	return spec, nil
}

// outputFilePath returns the path of the placeholder file emit() writes.
// Exposed (as a package-level helper) for tests that want to assert on the
// generated layout without depending on emit() internals.
func outputFilePath(outDir, pkg string) string {
	return filepath.Join(outDir, pkg+".go")
}

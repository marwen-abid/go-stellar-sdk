package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TestRenderPackage_ParsesAsGo asserts that the rendered source for a small
// spec is a syntactically valid Go file. Parsing is the cheapest end-to-end
// check that go/format.Source accepted our output and nothing about the
// imports / declarations is malformed.
func TestRenderPackage_ParsesAsGo(t *testing.T) {
	spec := contract.NewSpecFromEntries([]xdr.ScSpecEntry{
		mustFnEntry(t, "transfer", []xdr.ScSpecFunctionInputV0{
			{Name: "from", Type: simpleType(xdr.ScSpecTypeScSpecTypeAddress)},
			{Name: "to", Type: simpleType(xdr.ScSpecTypeScSpecTypeAddress)},
			{Name: "amount", Type: simpleType(xdr.ScSpecTypeScSpecTypeI128)},
		}, nil),
		mustStructEntry(t, "TransferEvent", []xdr.ScSpecUdtStructFieldV0{
			{Name: "from", Type: simpleType(xdr.ScSpecTypeScSpecTypeAddress)},
			{Name: "amount", Type: simpleType(xdr.ScSpecTypeScSpecTypeI128)},
		}),
		mustEnumEntry(t, "TokenStatus", []xdr.ScSpecUdtEnumCaseV0{
			{Name: "active", Value: 1},
			{Name: "frozen", Value: 2},
		}),
		mustErrorEnumEntry(t, "Error", []xdr.ScSpecUdtErrorEnumCaseV0{
			{Name: "InsufficientBalance", Value: 1},
		}),
	})

	src, err := renderPackage("token", spec)
	if err != nil {
		t.Fatalf("renderPackage: %v", err)
	}

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "token.go", src, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n--- source ---\n%s", err, src)
	}

	body := string(src)
	for _, want := range []string{
		"package token",
		"func (c *Client) Transfer(",
		"type TransferEvent struct",
		"type TokenStatus uint32",
		"type Error uint32",
		"func (e Error) Error() string",
		"func Spec() *contract.Spec",
		"TODO(T8.3)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in generated source:\n%s", want, body)
		}
	}
}

// TestRenderPackage_EmptySpec verifies that an empty spec still produces a
// minimal valid Go file (package decl + Client wrapper + Spec accessor). The
// generated package is useless to call but it has to compile so that
// pipelines that codegen-then-build don't fail on stub specs.
func TestRenderPackage_EmptySpec(t *testing.T) {
	spec := contract.NewSpecFromEntries(nil)
	src, err := renderPackage("empty", spec)
	if err != nil {
		t.Fatalf("renderPackage: %v", err)
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "empty.go", src, parser.ParseComments); err != nil {
		t.Fatalf("generated source does not parse: %v\n--- source ---\n%s", err, src)
	}
	body := string(src)
	for _, want := range []string{
		"package empty",
		"type Client struct",
		"func Spec() *contract.Spec",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in generated source:\n%s", want, body)
		}
	}
}

// TestRenderPackage_RoundTripsSpec checks that the base64-embedded spec
// decodes back into the same entries — the runtime contract.NewSpecFromBase64
// is the only consumer of that embed, so a round-trip is the right gate.
func TestRenderPackage_RoundTripsSpec(t *testing.T) {
	entries := []xdr.ScSpecEntry{
		mustFnEntry(t, "balance", []xdr.ScSpecFunctionInputV0{
			{Name: "id", Type: simpleType(xdr.ScSpecTypeScSpecTypeAddress)},
		}, []xdr.ScSpecTypeDef{simpleType(xdr.ScSpecTypeScSpecTypeI128)}),
	}
	spec := contract.NewSpecFromEntries(entries)
	src, err := renderPackage("tok", spec)
	if err != nil {
		t.Fatalf("renderPackage: %v", err)
	}

	// Pull specBase64 out of the source — cheap, deterministic.
	const marker = "const specBase64 = "
	idx := bytes.Index(src, []byte(marker))
	if idx < 0 {
		t.Fatal("specBase64 const not found in generated source")
	}
	start := idx + len(marker)
	end := bytes.IndexByte(src[start:], '\n')
	if end < 0 {
		t.Fatal("specBase64 const not terminated")
	}
	literal := string(src[start : start+end])
	// Strip the surrounding quotes; the literal is a Go string literal so
	// strconv.Unquote is the safe choice.
	unquoted, err := stripQuotes(literal)
	if err != nil {
		t.Fatalf("unquote specBase64: %v", err)
	}
	decoded, err := contract.NewSpecFromBase64(unquoted)
	if err != nil {
		t.Fatalf("decode embedded spec: %v", err)
	}
	if got := len(decoded.Entries()); got != len(entries) {
		t.Fatalf("round-trip entry count: got %d want %d", got, len(entries))
	}
}

func stripQuotes(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", &emitTestError{msg: "specBase64 literal is not a double-quoted string: " + s}
	}
	return s[1 : len(s)-1], nil
}

type emitTestError struct{ msg string }

func (e *emitTestError) Error() string { return e.msg }

// --- spec-entry builders local to this test file -------------------------

func simpleType(t xdr.ScSpecType) xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{Type: t}
}

func mustFnEntry(t *testing.T, name string, inputs []xdr.ScSpecFunctionInputV0, outputs []xdr.ScSpecTypeDef) xdr.ScSpecEntry {
	t.Helper()
	fn := xdr.ScSpecFunctionV0{
		Name:    xdr.ScSymbol(name),
		Inputs:  inputs,
		Outputs: outputs,
	}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryFunctionV0, fn)
	if err != nil {
		t.Fatalf("build fn entry: %v", err)
	}
	return e
}

func mustStructEntry(t *testing.T, name string, fields []xdr.ScSpecUdtStructFieldV0) xdr.ScSpecEntry {
	t.Helper()
	u := xdr.ScSpecUdtStructV0{Name: name, Fields: fields}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryUdtStructV0, u)
	if err != nil {
		t.Fatalf("build struct entry: %v", err)
	}
	return e
}

func mustEnumEntry(t *testing.T, name string, cases []xdr.ScSpecUdtEnumCaseV0) xdr.ScSpecEntry {
	t.Helper()
	u := xdr.ScSpecUdtEnumV0{Name: name, Cases: cases}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryUdtEnumV0, u)
	if err != nil {
		t.Fatalf("build enum entry: %v", err)
	}
	return e
}

func mustErrorEnumEntry(t *testing.T, name string, cases []xdr.ScSpecUdtErrorEnumCaseV0) xdr.ScSpecEntry {
	t.Helper()
	u := xdr.ScSpecUdtErrorEnumV0{Name: name, Cases: cases}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0, u)
	if err != nil {
		t.Fatalf("build error enum entry: %v", err)
	}
	return e
}

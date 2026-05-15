package main

import (
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// emitStruct renders a Soroban struct UDT as a Go struct declaration. Field
// names are exported, field types are mapped via goTypeFor, and each field
// carries the spec's doc string as a leading comment when present.
func emitStruct(udt xdr.ScSpecUdtStructV0, imps importSet) string {
	var b strings.Builder
	b.WriteString(docComment(udt.Doc, ""))
	fmt.Fprintf(&b, "type %s struct {\n", exportedIdent(udt.Name))
	for _, f := range udt.Fields {
		if c := docComment(f.Doc, "\t"); c != "" {
			b.WriteString(c)
		}
		fmt.Fprintf(&b, "\t%s %s\n", exportedIdent(f.Name), goTypeFor(f.Type, imps))
	}
	b.WriteString("}\n")
	return b.String()
}

// emitEnum renders an integer-tagged enum UDT (`#[contracttype] enum Foo { A
// = 1, B = 2 }`) as a `type FooKind uint32` plus exported const values. A
// stringer-style method is intentionally not emitted — that's caller territory.
func emitEnum(udt xdr.ScSpecUdtEnumV0) string {
	var b strings.Builder
	name := exportedIdent(udt.Name)
	b.WriteString(docComment(udt.Doc, ""))
	fmt.Fprintf(&b, "type %s uint32\n\n", name)
	b.WriteString("const (\n")
	for _, c := range udt.Cases {
		if doc := docComment(c.Doc, "\t"); doc != "" {
			b.WriteString(doc)
		}
		fmt.Fprintf(&b, "\t%s%s %s = %d\n", name, exportedIdent(c.Name), name, c.Value)
	}
	b.WriteString(")\n")
	return b.String()
}

// emitErrorEnum renders a #[contracterror] enum as a `type FooError uint32`
// plus a Go `Error() string` method so the values satisfy the error
// interface, mirroring viem-style typed contract errors.
func emitErrorEnum(udt xdr.ScSpecUdtErrorEnumV0) string {
	var b strings.Builder
	name := exportedIdent(udt.Name)
	b.WriteString(docComment(udt.Doc, ""))
	fmt.Fprintf(&b, "type %s uint32\n\n", name)
	b.WriteString("const (\n")
	for _, c := range udt.Cases {
		if doc := docComment(c.Doc, "\t"); doc != "" {
			b.WriteString(doc)
		}
		fmt.Fprintf(&b, "\t%s%s %s = %d\n", name, exportedIdent(c.Name), name, c.Value)
	}
	b.WriteString(")\n\n")
	// Error() method. Switch on the value so the message is helpful.
	fmt.Fprintf(&b, "func (e %s) Error() string {\n", name)
	b.WriteString("\tswitch e {\n")
	for _, c := range udt.Cases {
		fmt.Fprintf(&b, "\tcase %s%s:\n", name, exportedIdent(c.Name))
		fmt.Fprintf(&b, "\t\treturn %q\n", fmt.Sprintf("%s: %s", udt.Name, c.Name))
	}
	b.WriteString("\t}\n")
	fmt.Fprintf(&b, "\treturn fmt.Sprintf(%q, uint32(e))\n", udt.Name+": unknown(%d)")
	b.WriteString("}\n")
	return b.String()
}

// emitUnion renders a Rust-style sum-type enum (`#[contracttype] enum Foo {
// A, B(u32), C(u32, u32) }`) as a Go struct with an exported Kind field plus
// one optional payload slot per tuple case. We don't generate an interface
// hierarchy — that's heavier than the current marshaler needs.
func emitUnion(udt xdr.ScSpecUdtUnionV0, imps importSet) string {
	var b strings.Builder
	name := exportedIdent(udt.Name)
	kindType := name + "Kind"

	b.WriteString(docComment(udt.Doc, ""))
	fmt.Fprintf(&b, "type %s string\n\n", kindType)
	b.WriteString("const (\n")
	for _, c := range udt.Cases {
		caseName := unionCaseName(c)
		if caseName == "" {
			continue
		}
		fmt.Fprintf(&b, "\t%s%s %s = %q\n", name, caseName, kindType, caseName)
	}
	b.WriteString(")\n\n")

	fmt.Fprintf(&b, "type %s struct {\n", name)
	fmt.Fprintf(&b, "\tKind %s\n", kindType)
	for _, c := range udt.Cases {
		if c.Kind != xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0 || c.TupleCase == nil {
			continue
		}
		caseName := exportedIdent(c.TupleCase.Name)
		for i, t := range c.TupleCase.Type {
			suffix := ""
			if len(c.TupleCase.Type) > 1 {
				suffix = fmt.Sprintf("_%d", i)
			}
			fmt.Fprintf(&b, "\t%s%s %s\n", caseName, suffix, goTypeFor(t, imps))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// unionCaseName returns the exported case name of a union case regardless of
// whether it is a void or tuple variant. Returns "" for malformed entries.
func unionCaseName(c xdr.ScSpecUdtUnionCaseV0) string {
	switch c.Kind {
	case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0:
		if c.VoidCase == nil {
			return ""
		}
		return exportedIdent(c.VoidCase.Name)
	case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0:
		if c.TupleCase == nil {
			return ""
		}
		return exportedIdent(c.TupleCase.Name)
	}
	return ""
}

package main

import (
	"strings"
	"unicode"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// importSet collects the import paths a generated file needs. It is used by
// each per-emitter pass and merged before final assembly.
type importSet map[string]struct{}

func newImportSet() importSet { return importSet{} }

func (s importSet) add(path string) {
	if path == "" {
		return
	}
	s[path] = struct{}{}
}

func (s importSet) sortedPaths() []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	// Simple lexicographic order — gofmt groups stdlib vs. non-stdlib by
	// blank line, but go/format.Source preserves a single grouped block.
	// Sorting by path is enough for go/format to accept the file.
	sortStrings(out)
	return out
}

// sortStrings is a tiny insertion sort to avoid importing "sort" here, which
// would conflict with the importSet's own job of tracking imports. The slices
// we touch are tiny (≤10 entries).
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

// goTypeFor maps an xdr.ScSpecTypeDef onto the Go type the generated code
// uses for inputs, struct fields, and return values. It registers any
// imports the resulting type expression needs onto imps.
//
// The mapping is intentionally narrow: the cases listed below are the ones a
// real contract spec emits. Anything outside the list falls back to `any`
// (with the necessary import). Callers that need exhaustive coverage should
// teach this function — never silently widen.
func goTypeFor(ty xdr.ScSpecTypeDef, imps importSet) string {
	switch ty.Type {
	case xdr.ScSpecTypeScSpecTypeBool:
		return "bool"
	case xdr.ScSpecTypeScSpecTypeVoid:
		// void only ever appears as a return type; callers ignore it.
		return ""
	case xdr.ScSpecTypeScSpecTypeU32:
		return "uint32"
	case xdr.ScSpecTypeScSpecTypeI32:
		return "int32"
	case xdr.ScSpecTypeScSpecTypeU64, xdr.ScSpecTypeScSpecTypeTimepoint, xdr.ScSpecTypeScSpecTypeDuration:
		return "uint64"
	case xdr.ScSpecTypeScSpecTypeI64:
		return "int64"
	case xdr.ScSpecTypeScSpecTypeU128, xdr.ScSpecTypeScSpecTypeI128,
		xdr.ScSpecTypeScSpecTypeU256, xdr.ScSpecTypeScSpecTypeI256:
		imps.add("math/big")
		return "*big.Int"
	case xdr.ScSpecTypeScSpecTypeBytes:
		return "[]byte"
	case xdr.ScSpecTypeScSpecTypeBytesN:
		return "[]byte"
	case xdr.ScSpecTypeScSpecTypeString, xdr.ScSpecTypeScSpecTypeSymbol:
		return "string"
	case xdr.ScSpecTypeScSpecTypeAddress, xdr.ScSpecTypeScSpecTypeMuxedAddress:
		return "string"
	case xdr.ScSpecTypeScSpecTypeOption:
		if ty.Option == nil {
			return "any"
		}
		// Optionals reuse the inner Go type; the runtime spec marshal layer
		// treats Go `nil` (for nilable types) or the zero value (for value
		// types) as None. We don't introduce *T here because most inner
		// types (string, []byte, *big.Int, slices, maps) are already
		// nilable, and *uint32 is uncommon. If a contract needs *uint32
		// semantics, the user can post-process the generated file.
		return goTypeFor(ty.Option.ValueType, imps)
	case xdr.ScSpecTypeScSpecTypeVec:
		if ty.Vec == nil {
			return "[]any"
		}
		return "[]" + goTypeFor(ty.Vec.ElementType, imps)
	case xdr.ScSpecTypeScSpecTypeMap:
		if ty.Map == nil {
			return "map[any]any"
		}
		k := goTypeFor(ty.Map.KeyType, imps)
		v := goTypeFor(ty.Map.ValueType, imps)
		return "map[" + k + "]" + v
	case xdr.ScSpecTypeScSpecTypeTuple:
		// Tuples have no first-class Go counterpart. Surface as []any with
		// a comment from the caller — the runtime marshal layer accepts
		// []any for tuples.
		return "[]any"
	case xdr.ScSpecTypeScSpecTypeResult:
		// Result<T, E> in inputs is rare; surface as `any` so the file
		// compiles. Outputs are handled separately by the function emitter.
		return "any"
	case xdr.ScSpecTypeScSpecTypeUdt:
		if ty.Udt == nil {
			return "any"
		}
		return exportedIdent(ty.Udt.Name)
	}
	return "any"
}

// exportedIdent converts a Soroban identifier (snake_case, kebab-case, or
// already-CamelCase) into an exported Go identifier. The first rune is always
// upper-cased; subsequent runes after `_`, `-`, or a digit are upper-cased.
//
// Invalid leading characters (digits, symbols) are prefixed with `X_` so the
// result is always a valid Go identifier.
func exportedIdent(name string) string {
	if name == "" {
		return "X_"
	}
	var b strings.Builder
	b.Grow(len(name))
	upperNext := true
	for i, r := range name {
		switch {
		case r == '_' || r == '-' || r == ' ':
			upperNext = true
		case i == 0 && unicode.IsDigit(r):
			b.WriteString("X_")
			b.WriteRune(r)
			upperNext = false
		default:
			if upperNext {
				b.WriteRune(unicode.ToUpper(r))
			} else {
				b.WriteRune(r)
			}
			upperNext = false
		}
	}
	if b.Len() == 0 {
		return "X_"
	}
	return b.String()
}

// unexportedIdent returns the lower-camel-case form of name. Used for method
// parameter names so they don't collide with the exported field names on
// generated struct types.
func unexportedIdent(name string) string {
	id := exportedIdent(name)
	if id == "" {
		return id
	}
	runes := []rune(id)
	runes[0] = unicode.ToLower(runes[0])
	out := string(runes)
	if isGoReservedWord(out) {
		return out + "_"
	}
	return out
}

// isGoReservedWord matches Go's reserved keywords and a handful of predeclared
// identifiers we'd hate to shadow as a parameter name.
func isGoReservedWord(s string) bool {
	switch s {
	case "break", "default", "func", "interface", "select",
		"case", "defer", "go", "map", "struct",
		"chan", "else", "goto", "package", "switch",
		"const", "fallthrough", "if", "range", "type",
		"continue", "for", "import", "return", "var",
		"any", "bool", "byte", "error", "string",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64",
		"true", "false", "nil",
		"new", "make", "len", "cap", "append", "copy", "delete",
		"min", "max", "clear",
		"ctx": // would collide with the method's context parameter.
		return true
	}
	return false
}

// docComment renders a Soroban doc string as a `// ` comment block,
// indented to the supplied prefix. Returns an empty string when doc is empty.
func docComment(doc, indent string) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		b.WriteString(indent)
		b.WriteString("// ")
		b.WriteString(strings.TrimRight(line, " \t"))
		b.WriteString("\n")
	}
	return b.String()
}

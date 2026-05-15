package main

import (
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// emitFunc renders one Soroban function as a method on the generated *Client.
//
// The generated method:
//   - Takes context.Context first, then one parameter per spec input in
//     declaration order, mapped via goTypeFor.
//   - Builds map[string]any{...} of the inputs and forwards it to
//     contract.Client.Invoke, which in turn marshals them via the bundled
//     Spec.
//   - Returns the *contract.AssembledTransaction directly so the caller drives
//     the rest of the lifecycle (Sign / SignAndSend / Result). This matches
//     §4.11's example signature.
//
// Imports needed: context and the contract package — both registered onto
// imps so the file's import block carries them.
func emitFunc(fn xdr.ScSpecFunctionV0, imps importSet) string {
	imps.add("context")
	imps.add("github.com/stellar/go-stellar-sdk/contract")

	var b strings.Builder
	methodName := exportedIdent(string(fn.Name))

	b.WriteString(docComment(fn.Doc, ""))
	fmt.Fprintf(&b, "// %s invokes the %q function on the bound contract.\n", methodName, string(fn.Name))
	b.WriteString("//\n")
	b.WriteString("// The returned *contract.AssembledTransaction has already been simulated.\n")
	b.WriteString("// Call Result() for a read call, or Sign + SignAndSend + Wait for a write.\n")

	// Build the parameter list and arg map.
	params := []string{"ctx context.Context"}
	mapEntries := make([]string, 0, len(fn.Inputs))
	usedNames := map[string]int{} // disambiguate duplicate input names.
	for _, in := range fn.Inputs {
		pname := unexportedIdent(in.Name)
		if n := usedNames[pname]; n > 0 {
			pname = fmt.Sprintf("%s_%d", pname, n)
		}
		usedNames[unexportedIdent(in.Name)]++
		params = append(params, fmt.Sprintf("%s %s", pname, goTypeFor(in.Type, imps)))
		mapEntries = append(mapEntries, fmt.Sprintf("\t\t%q: %s,", in.Name, pname))
	}
	fmt.Fprintf(&b, "func (c *Client) %s(%s) (*contract.AssembledTransaction, error) {\n",
		methodName, strings.Join(params, ", "))
	if len(mapEntries) == 0 {
		fmt.Fprintf(&b, "\treturn c.inner.Invoke(ctx, %q, nil)\n", string(fn.Name))
	} else {
		b.WriteString("\targs := map[string]any{\n")
		for _, e := range mapEntries {
			b.WriteString(e)
			b.WriteString("\n")
		}
		b.WriteString("\t}\n")
		fmt.Fprintf(&b, "\treturn c.inner.Invoke(ctx, %q, args)\n", string(fn.Name))
	}
	b.WriteString("}\n")
	return b.String()
}

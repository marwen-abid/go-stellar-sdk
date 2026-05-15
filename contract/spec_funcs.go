package contract

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// FuncArgsToScVals marshals a name-keyed map of native Go values into the
// positional []xdr.ScVal slice expected by the host invocation for the named
// function. Each argument is converted via NativeToScVal using the declared
// input type from the spec.
//
// Returns an *Error with KindInvalidArgs if the function is unknown, if a
// required input is missing from args, if args contains extra keys not
// declared by the function, or if any per-argument marshal fails.
func (s *Spec) FuncArgsToScVals(name string, args map[string]any) ([]xdr.ScVal, error) {
	fn, err := s.lookupFunc(name)
	if err != nil {
		return nil, err
	}

	// Reject extra keys so callers learn about typos at marshal time rather
	// than silently dropping them.
	declared := make(map[string]struct{}, len(fn.Inputs))
	for _, in := range fn.Inputs {
		declared[in.Name] = struct{}{}
	}
	for k := range args {
		if _, ok := declared[k]; !ok {
			return nil, invalidArgsf("function %q: unexpected argument %q", name, k)
		}
	}

	out := make([]xdr.ScVal, len(fn.Inputs))
	for i, in := range fn.Inputs {
		v, ok := args[in.Name]
		if !ok {
			return nil, invalidArgsf("function %q: missing argument %q", name, in.Name)
		}
		sv, err := s.nativeToScVal(v, in.Type)
		if err != nil {
			return nil, fmt.Errorf("function %q arg %q: %w", name, in.Name, err)
		}
		out[i] = sv
	}
	return out, nil
}

// FuncResToNative converts an xdr.ScVal produced by invoking the named
// function back into a native Go value, using the function's declared output
// type. Functions declared with no output return nil regardless of the wire
// value (Soroban convention is ScvVoid in that case).
//
// Returns an *Error with KindInvalidArgs if the function is unknown or the
// underlying decoder rejects the value.
func (s *Spec) FuncResToNative(name string, v xdr.ScVal) (any, error) {
	fn, err := s.lookupFunc(name)
	if err != nil {
		return nil, err
	}
	if len(fn.Outputs) == 0 {
		return nil, nil
	}
	return s.scValToNative(v, fn.Outputs[0])
}

// lookupFunc returns the named function entry or an *Error if it is not a
// function in this spec.
func (s *Spec) lookupFunc(name string) (xdr.ScSpecFunctionV0, error) {
	entry, ok := s.lookup(name)
	if !ok || entry.Kind != xdr.ScSpecEntryKindScSpecEntryFunctionV0 || entry.FunctionV0 == nil {
		return xdr.ScSpecFunctionV0{}, invalidArgsf("function %q not found in spec", name)
	}
	return *entry.FunctionV0, nil
}

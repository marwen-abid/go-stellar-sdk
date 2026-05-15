package contract

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// fnEntryWithSig builds a function spec entry with the given inputs and
// optional output type.
func fnEntryWithSig(t *testing.T, name string, inputs []xdr.ScSpecFunctionInputV0, output *xdr.ScSpecTypeDef) xdr.ScSpecEntry {
	t.Helper()
	fn := xdr.ScSpecFunctionV0{
		Name:    xdr.ScSymbol(name),
		Inputs:  inputs,
		Outputs: []xdr.ScSpecTypeDef{},
	}
	if output != nil {
		fn.Outputs = []xdr.ScSpecTypeDef{*output}
	}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryFunctionV0, fn)
	require.NoError(t, err)
	return e
}

func input(name string, ty xdr.ScSpecType) xdr.ScSpecFunctionInputV0 {
	return xdr.ScSpecFunctionInputV0{Name: name, Type: xdr.ScSpecTypeDef{Type: ty}}
}

func TestFuncArgsToScVals_ZeroArgs(t *testing.T) {
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "init", nil, nil),
	})
	out, err := s.FuncArgsToScVals("init", nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestFuncArgsToScVals_HappyPath(t *testing.T) {
	out := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "add", []xdr.ScSpecFunctionInputV0{
			input("a", xdr.ScSpecTypeScSpecTypeU32),
			input("b", xdr.ScSpecTypeScSpecTypeI64),
			input("memo", xdr.ScSpecTypeScSpecTypeString),
			input("flag", xdr.ScSpecTypeScSpecTypeBool),
		}, &out),
	})

	args := map[string]any{
		"a":    uint32(7),
		"b":    int64(-42),
		"memo": "hi",
		"flag": true,
	}
	got, err := s.FuncArgsToScVals("add", args)
	require.NoError(t, err)
	require.Len(t, got, 4)

	// Verify positional order matches Inputs declaration order.
	require.Equal(t, xdr.ScValTypeScvU32, got[0].Type)
	require.Equal(t, uint32(7), uint32(got[0].MustU32()))
	require.Equal(t, xdr.ScValTypeScvI64, got[1].Type)
	require.Equal(t, int64(-42), int64(got[1].MustI64()))
	require.Equal(t, xdr.ScValTypeScvString, got[2].Type)
	require.Equal(t, "hi", string(got[2].MustStr()))
	require.Equal(t, xdr.ScValTypeScvBool, got[3].Type)
	require.Equal(t, true, got[3].MustB())
}

func TestFuncArgsToScVals_Errors(t *testing.T) {
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "double", []xdr.ScSpecFunctionInputV0{
			input("x", xdr.ScSpecTypeScSpecTypeU32),
		}, nil),
	})

	t.Run("unknown function", func(t *testing.T) {
		_, err := s.FuncArgsToScVals("missing", map[string]any{})
		require.Error(t, err)
		require.True(t, errors.Is(err, &Error{Kind: KindInvalidArgs}))
	})

	t.Run("missing arg", func(t *testing.T) {
		_, err := s.FuncArgsToScVals("double", map[string]any{})
		require.Error(t, err)
		require.True(t, errors.Is(err, &Error{Kind: KindInvalidArgs}))
		require.Contains(t, err.Error(), "missing argument")
	})

	t.Run("extra arg", func(t *testing.T) {
		_, err := s.FuncArgsToScVals("double", map[string]any{"x": uint32(1), "y": uint32(2)})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected argument")
	})

	t.Run("wrong type", func(t *testing.T) {
		_, err := s.FuncArgsToScVals("double", map[string]any{"x": "not-a-number"})
		require.Error(t, err)
		// Wrapped error from nativeToScVal still classifies as KindInvalidArgs.
		require.True(t, errors.Is(err, &Error{Kind: KindInvalidArgs}))
	})
}

func TestFuncResToNative_VoidReturn(t *testing.T) {
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "noop", nil, nil),
	})
	// No declared output: the wire value is ignored and we return nil.
	got, err := s.FuncResToNative("noop", xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestFuncResToNative_HappyPath(t *testing.T) {
	out := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU64}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "now", nil, &out),
	})

	u := xdr.Uint64(1700000000)
	sv := xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}
	got, err := s.FuncResToNative("now", sv)
	require.NoError(t, err)
	require.Equal(t, uint64(1700000000), got)
}

func TestFuncResToNative_VecReturn(t *testing.T) {
	elem := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32}
	out := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeVec,
		Vec:  &xdr.ScSpecTypeVec{ElementType: elem},
	}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "list", nil, &out),
	})

	// Round-trip via the encoder so we don't hand-build the inner ScVals.
	sv, err := s.NativeToScVal([]any{uint32(1), uint32(2), uint32(3)}, out)
	require.NoError(t, err)

	got, err := s.FuncResToNative("list", sv)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual([]any{uint32(1), uint32(2), uint32(3)}, got))
}

func TestFuncResToNative_UnknownFunction(t *testing.T) {
	s := NewSpecFromEntries(nil)
	_, err := s.FuncResToNative("ghost", xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	require.Error(t, err)
	require.True(t, errors.Is(err, &Error{Kind: KindInvalidArgs}))
}

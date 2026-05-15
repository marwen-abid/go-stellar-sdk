package contract

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// TestSpecCorpus is the Phase 2 closing round-trip corpus (T2.5). It exercises
// Spec.FuncArgsToScVals / FuncResToNative end-to-end against a single realistic
// handcrafted spec that mimics a SAC-style token contract: multi-argument
// functions, composite return types, and every supported type (primitive +
// composite + UDT struct / enum / error-enum).
//
// The Phase 2 unit tests in spec_marshal_test.go cover each converter in
// isolation; this file is the integration view: native args go in via the
// function-level API, ScVals are produced positionally, then a wire value
// flows back through the result decoder and we assert deep-equality on both
// sides. If any converter regresses, this test fails alongside the targeted
// one — giving us a single corpus to grow over time.
//
// Unions are covered via the `Action` UDT (T2.2a): a void case plus a
// tuple case with a single payload, exercised end-to-end through
// FuncArgsToScVals / FuncResToNative.

// addrAccount and addrContract are stable strkey fixtures reused across cases.
const (
	corpusAddrAccount  = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	corpusAddrContract = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"
)

// corpusSpec builds the shared spec used by all sub-tests. It declares:
//
//   - Balance        struct UDT (Address + I128)
//   - Color          enum UDT
//   - TokenError     error-enum UDT
//   - all_primitives function — every scalar primitive in one call
//   - composites     function — Option / Vec / Map / Tuple / BytesN / Result
//   - balance_of     function — UDT struct return
//   - status         function — UDT enum return
//   - fail           function — UDT error-enum return
func corpusSpec(t *testing.T) *Spec {
	t.Helper()

	ty := func(k xdr.ScSpecType) xdr.ScSpecTypeDef { return xdr.ScSpecTypeDef{Type: k} }

	balanceStruct := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
		UdtStructV0: &xdr.ScSpecUdtStructV0{
			Name: "Balance",
			Fields: []xdr.ScSpecUdtStructFieldV0{
				{Name: "owner", Type: ty(xdr.ScSpecTypeScSpecTypeAddress)},
				{Name: "amount", Type: ty(xdr.ScSpecTypeScSpecTypeI128)},
			},
		},
	}
	colorEnum := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtEnumV0,
		UdtEnumV0: &xdr.ScSpecUdtEnumV0{
			Name: "Color",
			Cases: []xdr.ScSpecUdtEnumCaseV0{
				{Name: "Red", Value: 0},
				{Name: "Green", Value: 1},
				{Name: "Blue", Value: 2},
			},
		},
	}
	tokenError := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0,
		UdtErrorEnumV0: &xdr.ScSpecUdtErrorEnumV0{
			Name: "TokenError",
			Cases: []xdr.ScSpecUdtErrorEnumCaseV0{
				{Name: "Unauthorized", Value: 1},
				{Name: "InsufficientBalance", Value: 2},
			},
		},
	}
	actionUnion := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtUnionV0,
		UdtUnionV0: &xdr.ScSpecUdtUnionV0{
			Name: "Action",
			Cases: []xdr.ScSpecUdtUnionCaseV0{
				{
					Kind:     xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0,
					VoidCase: &xdr.ScSpecUdtUnionCaseVoidV0{Name: "Noop"},
				},
				{
					Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0,
					TupleCase: &xdr.ScSpecUdtUnionCaseTupleV0{
						Name: "Increment",
						Type: []xdr.ScSpecTypeDef{ty(xdr.ScSpecTypeScSpecTypeU32)},
					},
				},
			},
		},
	}

	// all_primitives: every scalar Soroban type as a positional input. Return
	// is Void — the goal here is exercising the input path.
	allPrim := fnEntryWithSig(t, "all_primitives", []xdr.ScSpecFunctionInputV0{
		input("flag", xdr.ScSpecTypeScSpecTypeBool),
		input("u32v", xdr.ScSpecTypeScSpecTypeU32),
		input("i32v", xdr.ScSpecTypeScSpecTypeI32),
		input("u64v", xdr.ScSpecTypeScSpecTypeU64),
		input("i64v", xdr.ScSpecTypeScSpecTypeI64),
		input("tp", xdr.ScSpecTypeScSpecTypeTimepoint),
		input("dur", xdr.ScSpecTypeScSpecTypeDuration),
		input("u128v", xdr.ScSpecTypeScSpecTypeU128),
		input("i128v", xdr.ScSpecTypeScSpecTypeI128),
		input("u256v", xdr.ScSpecTypeScSpecTypeU256),
		input("i256v", xdr.ScSpecTypeScSpecTypeI256),
		input("blob", xdr.ScSpecTypeScSpecTypeBytes),
		input("name", xdr.ScSpecTypeScSpecTypeString),
		input("topic", xdr.ScSpecTypeScSpecTypeSymbol),
		input("who", xdr.ScSpecTypeScSpecTypeAddress),
	}, nil)

	// composites: Option / Vec / Map / Tuple / BytesN / Result. Return is the
	// Tuple, so we can also exercise FuncResToNative on a composite.
	bytes32 := xdr.ScSpecTypeDef{
		Type:   xdr.ScSpecTypeScSpecTypeBytesN,
		BytesN: &xdr.ScSpecTypeBytesN{N: 4},
	}
	optU32 := xdr.ScSpecTypeDef{
		Type:   xdr.ScSpecTypeScSpecTypeOption,
		Option: &xdr.ScSpecTypeOption{ValueType: ty(xdr.ScSpecTypeScSpecTypeU32)},
	}
	vecSym := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeVec,
		Vec:  &xdr.ScSpecTypeVec{ElementType: ty(xdr.ScSpecTypeScSpecTypeSymbol)},
	}
	mapSymU32 := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeMap,
		Map: &xdr.ScSpecTypeMap{
			KeyType:   ty(xdr.ScSpecTypeScSpecTypeSymbol),
			ValueType: ty(xdr.ScSpecTypeScSpecTypeU32),
		},
	}
	resOfBool := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeResult,
		Result: &xdr.ScSpecTypeResult{
			OkType:    ty(xdr.ScSpecTypeScSpecTypeBool),
			ErrorType: ty(xdr.ScSpecTypeScSpecTypeError),
		},
	}
	tupleRet := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeTuple,
		Tuple: &xdr.ScSpecTypeTuple{ValueTypes: []xdr.ScSpecTypeDef{
			ty(xdr.ScSpecTypeScSpecTypeU32),
			ty(xdr.ScSpecTypeScSpecTypeSymbol),
		}},
	}
	composites := fnEntryWithSig(t, "composites", []xdr.ScSpecFunctionInputV0{
		{Name: "maybe", Type: optU32},
		{Name: "tags", Type: vecSym},
		{Name: "counts", Type: mapSymU32},
		{Name: "checksum", Type: bytes32},
		{Name: "ok", Type: resOfBool},
	}, &tupleRet)

	// balance_of: address -> Balance struct.
	balanceRet := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Balance"},
	}
	balanceOf := fnEntryWithSig(t, "balance_of", []xdr.ScSpecFunctionInputV0{
		input("owner", xdr.ScSpecTypeScSpecTypeAddress),
	}, &balanceRet)

	// status: () -> Color (enum).
	colorRet := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Color"},
	}
	status := fnEntryWithSig(t, "status", nil, &colorRet)

	// fail: () -> TokenError (error-enum).
	errRet := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "TokenError"},
	}
	fail := fnEntryWithSig(t, "fail", nil, &errRet)

	// next_action: (Action) -> Action — exercises union as both input and
	// output through the function-level API.
	actionTy := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Action"},
	}
	nextAction := fnEntryWithSig(t, "next_action", []xdr.ScSpecFunctionInputV0{
		{Name: "a", Type: actionTy},
	}, &actionTy)

	return NewSpecFromEntries([]xdr.ScSpecEntry{
		balanceStruct, colorEnum, tokenError, actionUnion,
		allPrim, composites, balanceOf, status, fail, nextAction,
	})
}

func TestSpecCorpus_AllPrimitives(t *testing.T) {
	s := corpusSpec(t)
	args := map[string]any{
		"flag":  true,
		"u32v":  uint32(0xDEADBEEF),
		"i32v":  int32(-12345),
		"u64v":  uint64(1) << 40,
		"i64v":  int64(-1234567890123),
		"tp":    uint64(1700000000),
		"dur":   uint64(86400),
		"u128v": new(big.Int).Lsh(big.NewInt(1), 100),
		"i128v": new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 100)),
		"u256v": new(big.Int).Lsh(big.NewInt(1), 200),
		"i256v": new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 200)),
		"blob":  []byte{0x01, 0x02, 0x03, 0x04, 0x05},
		"name":  "stellar",
		"topic": "transfer",
		"who":   corpusAddrAccount,
	}

	scvals, err := s.FuncArgsToScVals("all_primitives", args)
	require.NoError(t, err)
	require.Len(t, scvals, len(args))

	// Positional order is the declaration order: walk fn.Inputs and decode
	// each ScVal back through ScValToNative against its declared type, then
	// assert deep-equality with the original argument.
	fn := s.Funcs()[0]
	for _, f := range s.Funcs() {
		if string(f.Name) == "all_primitives" {
			fn = f
			break
		}
	}
	for i, in := range fn.Inputs {
		got, err := s.ScValToNative(scvals[i], in.Type)
		require.NoError(t, err, "decode arg %q", in.Name)
		assertCorpusEqual(t, in.Name, args[in.Name], got)
	}
}

func TestSpecCorpus_Composites(t *testing.T) {
	s := corpusSpec(t)

	t.Run("option present", func(t *testing.T) {
		args := map[string]any{
			"maybe":    uint32(7),
			"tags":     []any{"a", "b", "c"},
			"counts":   map[string]any{"x": uint32(1), "y": uint32(2)},
			"checksum": []byte{0xDE, 0xAD, 0xBE, 0xEF},
			"ok":       true,
		}
		scvals, err := s.FuncArgsToScVals("composites", args)
		require.NoError(t, err)
		require.Len(t, scvals, 5)
		assertCompositeRoundTrip(t, s, "composites", args, scvals)
	})

	t.Run("option absent (nil)", func(t *testing.T) {
		args := map[string]any{
			"maybe":    nil,
			"tags":     []any{},
			"counts":   map[string]any{},
			"checksum": []byte{0x00, 0x00, 0x00, 0x00},
			"ok":       false,
		}
		scvals, err := s.FuncArgsToScVals("composites", args)
		require.NoError(t, err)
		assertCompositeRoundTrip(t, s, "composites", args, scvals)
	})

	t.Run("tuple return", func(t *testing.T) {
		// Build a tuple ScVal via the encoder and decode via FuncResToNative.
		tupleTy := xdr.ScSpecTypeDef{
			Type: xdr.ScSpecTypeScSpecTypeTuple,
			Tuple: &xdr.ScSpecTypeTuple{ValueTypes: []xdr.ScSpecTypeDef{
				{Type: xdr.ScSpecTypeScSpecTypeU32},
				{Type: xdr.ScSpecTypeScSpecTypeSymbol},
			}},
		}
		sv, err := s.NativeToScVal([]any{uint32(99), "transfer"}, tupleTy)
		require.NoError(t, err)
		got, err := s.FuncResToNative("composites", sv)
		require.NoError(t, err)
		require.Equal(t, []any{uint32(99), "transfer"}, got)
	})
}

func TestSpecCorpus_StructReturn(t *testing.T) {
	s := corpusSpec(t)

	// Args: marshal an Address arg, then build the Balance return via the
	// encoder and decode via FuncResToNative.
	args := map[string]any{"owner": corpusAddrAccount}
	scvals, err := s.FuncArgsToScVals("balance_of", args)
	require.NoError(t, err)
	require.Len(t, scvals, 1)
	require.Equal(t, xdr.ScValTypeScvAddress, scvals[0].Type)

	balanceTy := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Balance"},
	}
	want := map[string]any{
		"owner":  corpusAddrContract,
		"amount": big.NewInt(1_000_000),
	}
	sv, err := s.NativeToScVal(want, balanceTy)
	require.NoError(t, err)

	got, err := s.FuncResToNative("balance_of", sv)
	require.NoError(t, err)
	gotMap, ok := got.(map[string]any)
	require.True(t, ok, "expected map[string]any, got %T", got)
	require.Equal(t, corpusAddrContract, gotMap["owner"])
	require.Equal(t, 0, gotMap["amount"].(*big.Int).Cmp(big.NewInt(1_000_000)))
}

func TestSpecCorpus_EnumReturn(t *testing.T) {
	s := corpusSpec(t)

	colorTy := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Color"},
	}
	for _, name := range []string{"Red", "Green", "Blue"} {
		sv, err := s.NativeToScVal(name, colorTy)
		require.NoError(t, err)
		got, err := s.FuncResToNative("status", sv)
		require.NoError(t, err)
		require.Equal(t, name, got)
	}
}

func TestSpecCorpus_ErrorEnumReturn(t *testing.T) {
	s := corpusSpec(t)

	errTy := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "TokenError"},
	}
	for _, name := range []string{"Unauthorized", "InsufficientBalance"} {
		sv, err := s.NativeToScVal(name, errTy)
		require.NoError(t, err)
		got, err := s.FuncResToNative("fail", sv)
		require.NoError(t, err)
		require.Equal(t, name, got)
	}
}

func TestSpecCorpus_UnionRoundTrip(t *testing.T) {
	s := corpusSpec(t)

	cases := []map[string]any{
		{"tag": "Noop"},
		{"tag": "Increment", "values": []any{uint32(7)}},
	}
	for _, in := range cases {
		args := map[string]any{"a": in}
		scvals, err := s.FuncArgsToScVals("next_action", args)
		require.NoError(t, err)
		require.Len(t, scvals, 1)

		got, err := s.FuncResToNative("next_action", scvals[0])
		require.NoError(t, err)
		require.Equal(t, in, got)
	}
}

// assertCompositeRoundTrip decodes each positional ScVal back through
// ScValToNative against the declared input type and asserts deep-equality
// with the original arg. Used for the composites function which has too many
// arms for a single inline comparison.
func assertCompositeRoundTrip(t *testing.T, s *Spec, fnName string, args map[string]any, scvals []xdr.ScVal) {
	t.Helper()
	var fn xdr.ScSpecFunctionV0
	for _, f := range s.Funcs() {
		if string(f.Name) == fnName {
			fn = f
			break
		}
	}
	require.Equal(t, len(fn.Inputs), len(scvals))
	for i, in := range fn.Inputs {
		got, err := s.ScValToNative(scvals[i], in.Type)
		require.NoError(t, err, "decode arg %q", in.Name)
		assertCorpusEqual(t, in.Name, args[in.Name], got)
	}
}

// assertCorpusEqual is reflect.DeepEqual with two carve-outs that come from
// Spec's intentional decode shape:
//
//   - *big.Int values compare by Cmp, not by reflect.DeepEqual (which inspects
//     internal nat slices).
//   - Maps decode to map[string]any; we accept any map with equal entries.
func assertCorpusEqual(t *testing.T, name string, want, got any) {
	t.Helper()

	if wb, ok := want.(*big.Int); ok {
		gb, ok := got.(*big.Int)
		require.True(t, ok, "%s: expected *big.Int, got %T", name, got)
		require.Zero(t, wb.Cmp(gb), "%s: big.Int mismatch want=%s got=%s", name, wb, gb)
		return
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: round-trip mismatch\n want: %#v\n  got: %#v", name, want, got)
	}
}

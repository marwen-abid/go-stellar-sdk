package contract

import (
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// roundTrip runs v through NativeToScVal -> ScValToNative and returns the
// decoded native value. Failures return the first error.
func roundTrip(t *testing.T, s *Spec, v any, ty xdr.ScSpecTypeDef) (any, xdr.ScVal) {
	t.Helper()
	sv, err := s.NativeToScVal(v, ty)
	if err != nil {
		t.Fatalf("NativeToScVal(%v): %v", v, err)
	}
	out, err := s.ScValToNative(sv, ty)
	if err != nil {
		t.Fatalf("ScValToNative: %v", err)
	}
	return out, sv
}

func ty(t xdr.ScSpecType) xdr.ScSpecTypeDef { return xdr.ScSpecTypeDef{Type: t} }

func TestSpecMarshalPrimitives(t *testing.T) {
	s := NewSpecFromEntries(nil)

	cases := []struct {
		name string
		in   any
		ty   xdr.ScSpecTypeDef
		want any
	}{
		{"bool true", true, ty(xdr.ScSpecTypeScSpecTypeBool), true},
		{"bool false", false, ty(xdr.ScSpecTypeScSpecTypeBool), false},
		{"void", nil, ty(xdr.ScSpecTypeScSpecTypeVoid), nil},
		{"u32", uint32(42), ty(xdr.ScSpecTypeScSpecTypeU32), uint32(42)},
		{"u32 max", uint32(0xFFFFFFFF), ty(xdr.ScSpecTypeScSpecTypeU32), uint32(0xFFFFFFFF)},
		{"i32 neg", int32(-7), ty(xdr.ScSpecTypeScSpecTypeI32), int32(-7)},
		{"u64", uint64(1 << 40), ty(xdr.ScSpecTypeScSpecTypeU64), uint64(1 << 40)},
		{"i64 neg", int64(-1234567890), ty(xdr.ScSpecTypeScSpecTypeI64), int64(-1234567890)},
		{"timepoint", uint64(1700000000), ty(xdr.ScSpecTypeScSpecTypeTimepoint), uint64(1700000000)},
		{"duration", uint64(86400), ty(xdr.ScSpecTypeScSpecTypeDuration), uint64(86400)},
		{"string", "hello", ty(xdr.ScSpecTypeScSpecTypeString), "hello"},
		{"symbol", "transfer", ty(xdr.ScSpecTypeScSpecTypeSymbol), "transfer"},
		{"bytes", []byte{1, 2, 3, 4}, ty(xdr.ScSpecTypeScSpecTypeBytes), []byte{1, 2, 3, 4}},
		{"bytes empty", []byte{}, ty(xdr.ScSpecTypeScSpecTypeBytes), []byte{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := roundTrip(t, s, c.in, c.ty)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestSpecMarshalI128U128(t *testing.T) {
	s := NewSpecFromEntries(nil)

	cases := []struct {
		name string
		in   *big.Int
		ty   xdr.ScSpecTypeDef
	}{
		{"i128 zero", big.NewInt(0), ty(xdr.ScSpecTypeScSpecTypeI128)},
		{"i128 positive", big.NewInt(1234567890), ty(xdr.ScSpecTypeScSpecTypeI128)},
		{"i128 negative", big.NewInt(-9999), ty(xdr.ScSpecTypeScSpecTypeI128)},
		{"i128 max",
			new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 127), big.NewInt(1)),
			ty(xdr.ScSpecTypeScSpecTypeI128)},
		{"i128 min",
			new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 127)),
			ty(xdr.ScSpecTypeScSpecTypeI128)},
		{"u128 zero", big.NewInt(0), ty(xdr.ScSpecTypeScSpecTypeU128)},
		{"u128 max",
			new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)),
			ty(xdr.ScSpecTypeScSpecTypeU128)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := roundTrip(t, s, c.in, c.ty)
			got, ok := out.(*big.Int)
			if !ok {
				t.Fatalf("expected *big.Int, got %T", out)
			}
			if got.Cmp(c.in) != 0 {
				t.Fatalf("got %s, want %s", got, c.in)
			}
		})
	}
}

func TestSpecMarshalI256U256(t *testing.T) {
	s := NewSpecFromEntries(nil)

	cases := []struct {
		name string
		in   *big.Int
		ty   xdr.ScSpecTypeDef
	}{
		{"i256 zero", big.NewInt(0), ty(xdr.ScSpecTypeScSpecTypeI256)},
		{"i256 positive", new(big.Int).Lsh(big.NewInt(1), 200), ty(xdr.ScSpecTypeScSpecTypeI256)},
		{"i256 negative", new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 200)), ty(xdr.ScSpecTypeScSpecTypeI256)},
		{"i256 max", new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)), ty(xdr.ScSpecTypeScSpecTypeI256)},
		{"i256 min", new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 255)), ty(xdr.ScSpecTypeScSpecTypeI256)},
		{"u256 zero", big.NewInt(0), ty(xdr.ScSpecTypeScSpecTypeU256)},
		{"u256 max", new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), ty(xdr.ScSpecTypeScSpecTypeU256)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := roundTrip(t, s, c.in, c.ty)
			got, ok := out.(*big.Int)
			if !ok {
				t.Fatalf("expected *big.Int, got %T", out)
			}
			if got.Cmp(c.in) != 0 {
				t.Fatalf("got %s, want %s", got, c.in)
			}
		})
	}
}

func TestSpecMarshalAddress(t *testing.T) {
	s := NewSpecFromEntries(nil)
	const acct = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	const contract = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"

	for _, addr := range []string{acct, contract} {
		out, _ := roundTrip(t, s, addr, ty(xdr.ScSpecTypeScSpecTypeAddress))
		if out != addr {
			t.Fatalf("address round-trip: got %v, want %v", out, addr)
		}
	}
}

func TestSpecMarshalBytesN(t *testing.T) {
	s := NewSpecFromEntries(nil)
	bn := xdr.ScSpecTypeBytesN{N: 4}
	tt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeBytesN, BytesN: &bn}

	out, _ := roundTrip(t, s, []byte{1, 2, 3, 4}, tt)
	if !reflect.DeepEqual(out, []byte{1, 2, 3, 4}) {
		t.Fatalf("got %v", out)
	}

	// Wrong length should fail.
	_, err := s.NativeToScVal([]byte{1, 2, 3}, tt)
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected KindInvalidArgs, got %v", err)
	}
}

func TestSpecMarshalVec(t *testing.T) {
	s := NewSpecFromEntries(nil)
	vecOfU32 := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeVec,
		Vec: &xdr.ScSpecTypeVec{
			ElementType: ty(xdr.ScSpecTypeScSpecTypeU32),
		},
	}
	out, _ := roundTrip(t, s, []any{uint32(1), uint32(2), uint32(3)}, vecOfU32)
	want := []any{uint32(1), uint32(2), uint32(3)}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %#v, want %#v", out, want)
	}

	// Empty vec.
	out, _ = roundTrip(t, s, []any{}, vecOfU32)
	if !reflect.DeepEqual(out, []any{}) {
		t.Fatalf("empty vec: got %#v", out)
	}
}

func TestSpecMarshalMap(t *testing.T) {
	s := NewSpecFromEntries(nil)
	mapType := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeMap,
		Map: &xdr.ScSpecTypeMap{
			KeyType:   ty(xdr.ScSpecTypeScSpecTypeSymbol),
			ValueType: ty(xdr.ScSpecTypeScSpecTypeU32),
		},
	}
	in := map[string]any{"a": uint32(1), "b": uint32(2)}
	out, _ := roundTrip(t, s, in, mapType)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("got %#v, want %#v", out, in)
	}

	// Empty map.
	out, _ = roundTrip(t, s, map[string]any{}, mapType)
	if !reflect.DeepEqual(out, map[string]any{}) {
		t.Fatalf("empty map: got %#v", out)
	}
}

func TestSpecMarshalTuple(t *testing.T) {
	s := NewSpecFromEntries(nil)
	tupleType := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeTuple,
		Tuple: &xdr.ScSpecTypeTuple{
			ValueTypes: []xdr.ScSpecTypeDef{
				ty(xdr.ScSpecTypeScSpecTypeU32),
				ty(xdr.ScSpecTypeScSpecTypeString),
			},
		},
	}
	in := []any{uint32(42), "hello"}
	out, _ := roundTrip(t, s, in, tupleType)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("got %#v, want %#v", out, in)
	}
}

func TestSpecMarshalOption(t *testing.T) {
	s := NewSpecFromEntries(nil)
	optU32 := xdr.ScSpecTypeDef{
		Type:   xdr.ScSpecTypeScSpecTypeOption,
		Option: &xdr.ScSpecTypeOption{ValueType: ty(xdr.ScSpecTypeScSpecTypeU32)},
	}

	// None.
	out, _ := roundTrip(t, s, nil, optU32)
	if out != nil {
		t.Fatalf("none: got %v", out)
	}

	// Some(42).
	out, _ = roundTrip(t, s, uint32(42), optU32)
	if !reflect.DeepEqual(out, uint32(42)) {
		t.Fatalf("some: got %#v", out)
	}

	// Some via pointer.
	v := uint32(99)
	out, _ = roundTrip(t, s, &v, optU32)
	if !reflect.DeepEqual(out, uint32(99)) {
		t.Fatalf("some-ptr: got %#v", out)
	}
}

func TestSpecMarshalResult(t *testing.T) {
	s := NewSpecFromEntries(nil)
	resultU32 := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeResult,
		Result: &xdr.ScSpecTypeResult{
			OkType:    ty(xdr.ScSpecTypeScSpecTypeU32),
			ErrorType: ty(xdr.ScSpecTypeScSpecTypeError),
		},
	}
	out, _ := roundTrip(t, s, uint32(7), resultU32)
	if !reflect.DeepEqual(out, uint32(7)) {
		t.Fatalf("got %#v", out)
	}
}

func TestSpecMarshalUDTStruct(t *testing.T) {
	udt := xdr.ScSpecUdtStructV0{
		Name: "Point",
		Fields: []xdr.ScSpecUdtStructFieldV0{
			{Name: "x", Type: ty(xdr.ScSpecTypeScSpecTypeU32)},
			{Name: "y", Type: ty(xdr.ScSpecTypeScSpecTypeU32)},
		},
	}
	entry := xdr.ScSpecEntry{
		Kind:        xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
		UdtStructV0: &udt,
	}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{entry})

	tt := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Point"},
	}
	in := map[string]any{"x": uint32(3), "y": uint32(4)}
	out, _ := roundTrip(t, s, in, tt)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("got %#v, want %#v", out, in)
	}

	// Missing field should fail.
	_, err := s.NativeToScVal(map[string]any{"x": uint32(3)}, tt)
	if err == nil {
		t.Fatalf("expected error for missing field")
	}
}

func TestSpecMarshalUDTEnum(t *testing.T) {
	udt := xdr.ScSpecUdtEnumV0{
		Name: "Color",
		Cases: []xdr.ScSpecUdtEnumCaseV0{
			{Name: "Red", Value: 0},
			{Name: "Green", Value: 1},
			{Name: "Blue", Value: 2},
		},
	}
	entry := xdr.ScSpecEntry{
		Kind:      xdr.ScSpecEntryKindScSpecEntryUdtEnumV0,
		UdtEnumV0: &udt,
	}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{entry})

	tt := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Color"},
	}
	out, _ := roundTrip(t, s, "Green", tt)
	if out != "Green" {
		t.Fatalf("got %v", out)
	}

	// Numeric input also accepted.
	out, _ = roundTrip(t, s, uint32(2), tt)
	if out != "Blue" {
		t.Fatalf("numeric: got %v", out)
	}

	// Unknown case.
	_, err := s.NativeToScVal("Yellow", tt)
	if err == nil {
		t.Fatalf("expected error for unknown case")
	}
}

func TestSpecMarshalUDTErrorEnum(t *testing.T) {
	udt := xdr.ScSpecUdtErrorEnumV0{
		Name: "Err",
		Cases: []xdr.ScSpecUdtErrorEnumCaseV0{
			{Name: "NotFound", Value: 1},
			{Name: "BadRequest", Value: 2},
		},
	}
	entry := xdr.ScSpecEntry{
		Kind:           xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0,
		UdtErrorEnumV0: &udt,
	}
	s := NewSpecFromEntries([]xdr.ScSpecEntry{entry})

	tt := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Err"},
	}
	out, _ := roundTrip(t, s, "BadRequest", tt)
	if out != "BadRequest" {
		t.Fatalf("got %v", out)
	}
}

func TestSpecMarshalErrors(t *testing.T) {
	s := NewSpecFromEntries(nil)

	// Wrong native type.
	_, err := s.NativeToScVal("not-a-bool", ty(xdr.ScSpecTypeScSpecTypeBool))
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected KindInvalidArgs, got %v", err)
	}

	// Overflow.
	_, err = s.NativeToScVal(int64(1<<33), ty(xdr.ScSpecTypeScSpecTypeU32))
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected overflow KindInvalidArgs, got %v", err)
	}

	// Negative into unsigned.
	_, err = s.NativeToScVal(int32(-1), ty(xdr.ScSpecTypeScSpecTypeU32))
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected negative KindInvalidArgs, got %v", err)
	}

	// ScVal type mismatch on decode.
	good := xdr.ScvBool(true)
	_, err = s.ScValToNative(good, ty(xdr.ScSpecTypeScSpecTypeU32))
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected decode mismatch KindInvalidArgs, got %v", err)
	}

	// Unknown UDT.
	tt := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Nope"},
	}
	_, err = s.NativeToScVal(map[string]any{}, tt)
	if err == nil || !errors.Is(err, &Error{Kind: KindInvalidArgs}) {
		t.Fatalf("expected unknown udt KindInvalidArgs, got %v", err)
	}
}

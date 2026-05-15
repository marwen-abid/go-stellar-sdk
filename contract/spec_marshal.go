package contract

import (
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// NativeToScVal converts a Go native value to an xdr.ScVal using ty as the
// expected target type. The expected-type parameter is necessary because the
// same Go value (e.g. an int) maps onto multiple Soroban types (U32/I32/U64/I64/I128/...).
//
// Supported targets, per design doc §6 T2.2:
//
//   - Scalars:  Val, Bool, Void, Error, U32, I32, U64, I64, Timepoint, Duration,
//     U128, I128, U256, I256, Bytes, BytesN, String, Symbol, Address.
//   - Composites: Option, Result, Vec, Map, Tuple.
//   - UDT (struct/enum/error-enum) resolved against this Spec's entries.
//
// Cross-contract UDT resolution via a global registry is T2.4's job and is
// not performed here.
//
// Union UDTs (T2.2a) use the JS-SDK-compatible native shape
// `map[string]any{"tag": "<CaseName>", "values": []any{...}}`. The wire
// representation is an ScVec whose first element is the ScSymbol tag and
// whose remaining elements are the tuple-case payload values (void cases
// produce a single-element vec).
func (s *Spec) NativeToScVal(v any, ty xdr.ScSpecTypeDef) (xdr.ScVal, error) {
	return s.nativeToScVal(v, ty)
}

// ScValToNative converts an xdr.ScVal back to a Go native value, using ty as
// the expected source type. Returned concrete types:
//
//   - Bool        -> bool
//   - U32/I32     -> uint32/int32
//   - U64/I64     -> uint64/int64
//   - Timepoint   -> uint64
//   - Duration    -> uint64
//   - U128/I128   -> *big.Int
//   - U256/I256   -> *big.Int
//   - Bytes       -> []byte
//   - BytesN      -> []byte
//   - String      -> string
//   - Symbol      -> string
//   - Address     -> string (strkey)
//   - Void        -> nil
//   - Error       -> *xdr.ScError
//   - Option<T>   -> nil or the decoded T
//   - Result<T,E> -> the decoded T (Err arm is surfaced as a contract revert
//     at a higher layer; here we decode whichever arm the ScVal carries).
//   - Vec<T>      -> []any
//   - Map<K,V>    -> map[K-native]V-native (string-keyed when K is symbol/string)
//   - Tuple       -> []any
//   - UDT struct  -> map[string]any
//   - UDT enum    -> string (case name)
//   - UDT error   -> string (case name)
func (s *Spec) ScValToNative(v xdr.ScVal, ty xdr.ScSpecTypeDef) (any, error) {
	return s.scValToNative(v, ty)
}

// ---------- encoder ----------

func (s *Spec) nativeToScVal(v any, ty xdr.ScSpecTypeDef) (xdr.ScVal, error) {
	switch ty.Type {
	case xdr.ScSpecTypeScSpecTypeVal:
		// Passthrough: caller already produced an xdr.ScVal.
		if sv, ok := v.(xdr.ScVal); ok {
			return sv, nil
		}
		return xdr.ScVal{}, invalidArgsf("expected xdr.ScVal for ScSpecTypeVal, got %T", v)

	case xdr.ScSpecTypeScSpecTypeBool:
		b, ok := v.(bool)
		if !ok {
			return xdr.ScVal{}, invalidArgsf("expected bool, got %T", v)
		}
		return xdr.ScvBool(b), nil

	case xdr.ScSpecTypeScSpecTypeVoid:
		if v != nil {
			return xdr.ScVal{}, invalidArgsf("expected nil for void, got %T", v)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil

	case xdr.ScSpecTypeScSpecTypeError:
		errVal, ok := v.(xdr.ScError)
		if !ok {
			return xdr.ScVal{}, invalidArgsf("expected xdr.ScError, got %T", v)
		}
		ev := errVal
		return xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &ev}, nil

	case xdr.ScSpecTypeScSpecTypeU32:
		n, err := toUint(v, 32)
		if err != nil {
			return xdr.ScVal{}, err
		}
		u := xdr.Uint32(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil

	case xdr.ScSpecTypeScSpecTypeI32:
		n, err := toInt(v, 32)
		if err != nil {
			return xdr.ScVal{}, err
		}
		i := xdr.Int32(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &i}, nil

	case xdr.ScSpecTypeScSpecTypeU64:
		n, err := toUint(v, 64)
		if err != nil {
			return xdr.ScVal{}, err
		}
		u := xdr.Uint64(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}, nil

	case xdr.ScSpecTypeScSpecTypeI64:
		n, err := toInt(v, 64)
		if err != nil {
			return xdr.ScVal{}, err
		}
		i := xdr.Int64(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &i}, nil

	case xdr.ScSpecTypeScSpecTypeTimepoint:
		n, err := toUint(v, 64)
		if err != nil {
			return xdr.ScVal{}, err
		}
		t := xdr.TimePoint(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &t}, nil

	case xdr.ScSpecTypeScSpecTypeDuration:
		n, err := toUint(v, 64)
		if err != nil {
			return xdr.ScVal{}, err
		}
		d := xdr.Duration(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil

	case xdr.ScSpecTypeScSpecTypeU128:
		bi, err := toBigInt(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScvU128(bi)

	case xdr.ScSpecTypeScSpecTypeI128:
		bi, err := toBigInt(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScvI128(bi)

	case xdr.ScSpecTypeScSpecTypeU256:
		bi, err := toBigInt(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		parts, err := u256Parts(bi)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeI256:
		bi, err := toBigInt(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		parts, err := i256Parts(bi)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeBytes:
		b, err := toBytes(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScvBytes(b), nil

	case xdr.ScSpecTypeScSpecTypeBytesN:
		b, err := toBytes(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		if ty.BytesN == nil {
			return xdr.ScVal{}, invalidArgsf("BytesN spec missing length")
		}
		want := int(uint32(ty.BytesN.N))
		if len(b) != want {
			return xdr.ScVal{}, invalidArgsf("BytesN expected %d bytes, got %d", want, len(b))
		}
		return xdr.ScvBytes(b), nil

	case xdr.ScSpecTypeScSpecTypeString:
		str, ok := v.(string)
		if !ok {
			if bs, ok2 := v.([]byte); ok2 {
				return xdr.ScvString(string(bs)), nil
			}
			return xdr.ScVal{}, invalidArgsf("expected string for ScSpecTypeString, got %T", v)
		}
		return xdr.ScvString(str), nil

	case xdr.ScSpecTypeScSpecTypeSymbol:
		str, ok := v.(string)
		if !ok {
			return xdr.ScVal{}, invalidArgsf("expected string for ScSpecTypeSymbol, got %T", v)
		}
		return xdr.ScvSymbol(str)

	case xdr.ScSpecTypeScSpecTypeAddress, xdr.ScSpecTypeScSpecTypeMuxedAddress:
		switch x := v.(type) {
		case string:
			return xdr.ScvAddress(x)
		case xdr.ScAddress:
			a := x
			return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &a}, nil
		default:
			return xdr.ScVal{}, invalidArgsf("expected string or xdr.ScAddress for address, got %T", v)
		}

	case xdr.ScSpecTypeScSpecTypeOption:
		if ty.Option == nil {
			return xdr.ScVal{}, invalidArgsf("option spec missing inner type")
		}
		if isNil(v) {
			return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
		}
		// If v is a non-nil pointer, dereference.
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer {
			v = rv.Elem().Interface()
		}
		return s.nativeToScVal(v, ty.Option.ValueType)

	case xdr.ScSpecTypeScSpecTypeResult:
		// Result<T,E>: encode the ok value as T. Err encoding is a higher-layer
		// concern (revert decoding); a caller that wants to send an Err must
		// produce an ScError and use ScSpecTypeError directly.
		if ty.Result == nil {
			return xdr.ScVal{}, invalidArgsf("result spec missing arm types")
		}
		return s.nativeToScVal(v, ty.Result.OkType)

	case xdr.ScSpecTypeScSpecTypeVec:
		if ty.Vec == nil {
			return xdr.ScVal{}, invalidArgsf("vec spec missing element type")
		}
		items, err := toSlice(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		out := make([]xdr.ScVal, len(items))
		for i, item := range items {
			ev, err := s.nativeToScVal(item, ty.Vec.ElementType)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("vec[%d]: %w", i, err)
			}
			out[i] = ev
		}
		return xdr.ScvVec(out...), nil

	case xdr.ScSpecTypeScSpecTypeMap:
		if ty.Map == nil {
			return xdr.ScVal{}, invalidArgsf("map spec missing key/value types")
		}
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Map {
			return xdr.ScVal{}, invalidArgsf("expected map for ScSpecTypeMap, got %T", v)
		}
		entries := make([]xdr.ScMapEntry, 0, rv.Len())
		for _, mk := range rv.MapKeys() {
			kv, err := s.nativeToScVal(mk.Interface(), ty.Map.KeyType)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map key: %w", err)
			}
			vv, err := s.nativeToScVal(rv.MapIndex(mk).Interface(), ty.Map.ValueType)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map value: %w", err)
			}
			entries = append(entries, xdr.ScMapEntry{Key: kv, Val: vv})
		}
		sortMapEntries(entries)
		m := xdr.ScMap(entries)
		pm := &m
		return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &pm}, nil

	case xdr.ScSpecTypeScSpecTypeTuple:
		if ty.Tuple == nil {
			return xdr.ScVal{}, invalidArgsf("tuple spec missing value types")
		}
		items, err := toSlice(v)
		if err != nil {
			return xdr.ScVal{}, err
		}
		if len(items) != len(ty.Tuple.ValueTypes) {
			return xdr.ScVal{}, invalidArgsf("tuple arity mismatch: want %d, got %d",
				len(ty.Tuple.ValueTypes), len(items))
		}
		out := make([]xdr.ScVal, len(items))
		for i, item := range items {
			ev, err := s.nativeToScVal(item, ty.Tuple.ValueTypes[i])
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("tuple[%d]: %w", i, err)
			}
			out[i] = ev
		}
		return xdr.ScvVec(out...), nil

	case xdr.ScSpecTypeScSpecTypeUdt:
		if ty.Udt == nil {
			return xdr.ScVal{}, invalidArgsf("udt spec missing name")
		}
		return s.udtToScVal(v, ty.Udt.Name)
	}

	return xdr.ScVal{}, invalidArgsf("unsupported ScSpecType: %s", ty.Type)
}

func (s *Spec) udtToScVal(v any, name string) (xdr.ScVal, error) {
	entry, ok := s.lookup(name)
	if !ok {
		return xdr.ScVal{}, invalidArgsf("udt %q not found in spec", name)
	}
	switch entry.Kind {
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		return s.structToScVal(v, entry.UdtStructV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		return enumToScVal(v, entry.UdtEnumV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0:
		return errorEnumToScVal(v, entry.UdtErrorEnumV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		return s.unionToScVal(v, entry.UdtUnionV0)
	}
	return xdr.ScVal{}, invalidArgsf("udt %q has unsupported kind %s", name, entry.Kind)
}

func (s *Spec) unionToScVal(v any, udt *xdr.ScSpecUdtUnionV0) (xdr.ScVal, error) {
	if udt == nil {
		return xdr.ScVal{}, invalidArgsf("union udt is nil")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return xdr.ScVal{}, invalidArgsf("expected map[string]any for union %q, got %T", udt.Name, v)
	}
	tagAny, ok := m["tag"]
	if !ok {
		return xdr.ScVal{}, invalidArgsf("union %q missing \"tag\" field", udt.Name)
	}
	tag, ok := tagAny.(string)
	if !ok {
		return xdr.ScVal{}, invalidArgsf("union %q \"tag\" must be string, got %T", udt.Name, tagAny)
	}
	for _, c := range udt.Cases {
		switch c.Kind {
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0:
			if c.VoidCase == nil || c.VoidCase.Name != tag {
				continue
			}
			if vs, present := m["values"]; present && !isEmptyValues(vs) {
				return xdr.ScVal{}, invalidArgsf("union %q void case %q expects no values", udt.Name, tag)
			}
			key, err := xdr.ScvSymbol(tag)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("union %q tag: %w", udt.Name, err)
			}
			return xdr.ScvVec(key), nil
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0:
			if c.TupleCase == nil || c.TupleCase.Name != tag {
				continue
			}
			types := c.TupleCase.Type
			var values []any
			if vs, present := m["values"]; present {
				vals, err := toSlice(vs)
				if err != nil {
					return xdr.ScVal{}, fmt.Errorf("union %q case %q values: %w", udt.Name, tag, err)
				}
				values = vals
			}
			if len(values) != len(types) {
				return xdr.ScVal{}, invalidArgsf("union %q case %q expects %d values, got %d",
					udt.Name, tag, len(types), len(values))
			}
			key, err := xdr.ScvSymbol(tag)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("union %q tag: %w", udt.Name, err)
			}
			out := make([]xdr.ScVal, 0, len(values)+1)
			out = append(out, key)
			for i, val := range values {
				ev, err := s.nativeToScVal(val, types[i])
				if err != nil {
					return xdr.ScVal{}, fmt.Errorf("union %q case %q values[%d]: %w", udt.Name, tag, i, err)
				}
				out = append(out, ev)
			}
			return xdr.ScvVec(out...), nil
		}
	}
	return xdr.ScVal{}, invalidArgsf("union %q has no case %q", udt.Name, tag)
}

func isEmptyValues(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return rv.Len() == 0
	}
	return false
}

func (s *Spec) structToScVal(v any, udt *xdr.ScSpecUdtStructV0) (xdr.ScVal, error) {
	if udt == nil {
		return xdr.ScVal{}, invalidArgsf("struct udt is nil")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return xdr.ScVal{}, invalidArgsf("expected map[string]any for struct %q, got %T", udt.Name, v)
	}
	// Fields ordered lexicographically by name in canonical encoding.
	byField := make(map[string]xdr.ScSpecUdtStructFieldV0, len(udt.Fields))
	names := make([]string, len(udt.Fields))
	for i, f := range udt.Fields {
		names[i] = f.Name
		byField[f.Name] = f
	}
	sort.Strings(names)

	entries := make([]xdr.ScMapEntry, 0, len(udt.Fields))
	for _, fname := range names {
		field := byField[fname]
		fv, present := m[fname]
		if !present {
			return xdr.ScVal{}, invalidArgsf("struct %q missing field %q", udt.Name, fname)
		}
		val, err := s.nativeToScVal(fv, field.Type)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("struct %q field %q: %w", udt.Name, fname, err)
		}
		keySym := xdr.ScSymbol(fname)
		entries = append(entries, xdr.ScMapEntry{
			Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &keySym},
			Val: val,
		})
	}
	pm := xdr.ScMap(entries)
	ppm := &pm
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &ppm}, nil
}

func enumToScVal(v any, udt *xdr.ScSpecUdtEnumV0) (xdr.ScVal, error) {
	if udt == nil {
		return xdr.ScVal{}, invalidArgsf("enum udt is nil")
	}
	switch x := v.(type) {
	case string:
		for _, c := range udt.Cases {
			if c.Name == x {
				u := xdr.Uint32(c.Value)
				return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil
			}
		}
		return xdr.ScVal{}, invalidArgsf("enum %q has no case %q", udt.Name, x)
	default:
		n, err := toUint(v, 32)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("enum %q: %w", udt.Name, err)
		}
		for _, c := range udt.Cases {
			if uint32(c.Value) == uint32(n) {
				u := xdr.Uint32(n)
				return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil
			}
		}
		return xdr.ScVal{}, invalidArgsf("enum %q has no case with value %d", udt.Name, n)
	}
}

func errorEnumToScVal(v any, udt *xdr.ScSpecUdtErrorEnumV0) (xdr.ScVal, error) {
	if udt == nil {
		return xdr.ScVal{}, invalidArgsf("error-enum udt is nil")
	}
	var code uint32
	switch x := v.(type) {
	case string:
		found := false
		for _, c := range udt.Cases {
			if c.Name == x {
				code = uint32(c.Value)
				found = true
				break
			}
		}
		if !found {
			return xdr.ScVal{}, invalidArgsf("error-enum %q has no case %q", udt.Name, x)
		}
	default:
		n, err := toUint(v, 32)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("error-enum %q: %w", udt.Name, err)
		}
		code = uint32(n)
	}
	cc := xdr.Uint32(code)
	se := xdr.ScError{Type: xdr.ScErrorTypeSceContract, ContractCode: &cc}
	return xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &se}, nil
}

// ---------- decoder ----------

func (s *Spec) scValToNative(v xdr.ScVal, ty xdr.ScSpecTypeDef) (any, error) {
	// Option<T>: void on the wire decodes to nil regardless of inner type.
	if ty.Type == xdr.ScSpecTypeScSpecTypeOption {
		if v.Type == xdr.ScValTypeScvVoid {
			return nil, nil
		}
		if ty.Option == nil {
			return nil, invalidArgsf("option spec missing inner type")
		}
		return s.scValToNative(v, ty.Option.ValueType)
	}
	if ty.Type == xdr.ScSpecTypeScSpecTypeResult {
		if ty.Result == nil {
			return nil, invalidArgsf("result spec missing arm types")
		}
		// Decode against ok arm; mismatched ScVal types will be surfaced by
		// the recursive call below.
		return s.scValToNative(v, ty.Result.OkType)
	}

	switch ty.Type {
	case xdr.ScSpecTypeScSpecTypeVal:
		return v, nil

	case xdr.ScSpecTypeScSpecTypeBool:
		if v.Type != xdr.ScValTypeScvBool {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return v.MustB(), nil

	case xdr.ScSpecTypeScSpecTypeVoid:
		if v.Type != xdr.ScValTypeScvVoid {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return nil, nil

	case xdr.ScSpecTypeScSpecTypeError:
		if v.Type != xdr.ScValTypeScvError {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		e := v.MustError()
		return &e, nil

	case xdr.ScSpecTypeScSpecTypeU32:
		if v.Type != xdr.ScValTypeScvU32 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return uint32(v.MustU32()), nil

	case xdr.ScSpecTypeScSpecTypeI32:
		if v.Type != xdr.ScValTypeScvI32 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return int32(v.MustI32()), nil

	case xdr.ScSpecTypeScSpecTypeU64:
		if v.Type != xdr.ScValTypeScvU64 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return uint64(v.MustU64()), nil

	case xdr.ScSpecTypeScSpecTypeI64:
		if v.Type != xdr.ScValTypeScvI64 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return int64(v.MustI64()), nil

	case xdr.ScSpecTypeScSpecTypeTimepoint:
		if v.Type != xdr.ScValTypeScvTimepoint {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return uint64(*v.Timepoint), nil

	case xdr.ScSpecTypeScSpecTypeDuration:
		if v.Type != xdr.ScValTypeScvDuration {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return uint64(*v.Duration), nil

	case xdr.ScSpecTypeScSpecTypeU128:
		if v.Type != xdr.ScValTypeScvU128 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return u128ToBigInt(v.MustU128()), nil

	case xdr.ScSpecTypeScSpecTypeI128:
		if v.Type != xdr.ScValTypeScvI128 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return i128ToBigInt(v.MustI128()), nil

	case xdr.ScSpecTypeScSpecTypeU256:
		if v.Type != xdr.ScValTypeScvU256 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return u256ToBigInt(v.MustU256()), nil

	case xdr.ScSpecTypeScSpecTypeI256:
		if v.Type != xdr.ScValTypeScvI256 {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return i256ToBigInt(v.MustI256()), nil

	case xdr.ScSpecTypeScSpecTypeBytes:
		if v.Type != xdr.ScValTypeScvBytes {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return []byte(v.MustBytes()), nil

	case xdr.ScSpecTypeScSpecTypeBytesN:
		if v.Type != xdr.ScValTypeScvBytes {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		b := []byte(v.MustBytes())
		if ty.BytesN == nil {
			return nil, invalidArgsf("BytesN spec missing length")
		}
		if len(b) != int(uint32(ty.BytesN.N)) {
			return nil, invalidArgsf("BytesN expected %d bytes, got %d", ty.BytesN.N, len(b))
		}
		return b, nil

	case xdr.ScSpecTypeScSpecTypeString:
		if v.Type != xdr.ScValTypeScvString {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return string(v.MustStr()), nil

	case xdr.ScSpecTypeScSpecTypeSymbol:
		if v.Type != xdr.ScValTypeScvSymbol {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		return string(v.MustSym()), nil

	case xdr.ScSpecTypeScSpecTypeAddress, xdr.ScSpecTypeScSpecTypeMuxedAddress:
		if v.Type != xdr.ScValTypeScvAddress {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		addr := v.MustAddress()
		str, err := addr.String()
		if err != nil {
			return nil, invalidArgsf("address: %v", err)
		}
		return str, nil

	case xdr.ScSpecTypeScSpecTypeVec:
		if v.Type != xdr.ScValTypeScvVec {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		if ty.Vec == nil {
			return nil, invalidArgsf("vec spec missing element type")
		}
		vec := v.MustVec()
		if vec == nil {
			return []any{}, nil
		}
		out := make([]any, len(*vec))
		for i, item := range *vec {
			ev, err := s.scValToNative(item, ty.Vec.ElementType)
			if err != nil {
				return nil, fmt.Errorf("vec[%d]: %w", i, err)
			}
			out[i] = ev
		}
		return out, nil

	case xdr.ScSpecTypeScSpecTypeMap:
		if v.Type != xdr.ScValTypeScvMap {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		if ty.Map == nil {
			return nil, invalidArgsf("map spec missing key/value types")
		}
		mp := v.MustMap()
		out := make(map[string]any)
		if mp == nil {
			return out, nil
		}
		stringKeyed := isStringKeyedMap(ty.Map.KeyType)
		if !stringKeyed {
			// Fall back to []any of [k, v] pairs when the key type is not
			// directly representable as a Go map key string. Keeps the
			// surface narrow for T2.2; richer support can come later.
			pairs := make([]any, 0, len(*mp))
			for _, ent := range *mp {
				k, err := s.scValToNative(ent.Key, ty.Map.KeyType)
				if err != nil {
					return nil, fmt.Errorf("map key: %w", err)
				}
				val, err := s.scValToNative(ent.Val, ty.Map.ValueType)
				if err != nil {
					return nil, fmt.Errorf("map value: %w", err)
				}
				pairs = append(pairs, [2]any{k, val})
			}
			return pairs, nil
		}
		for _, ent := range *mp {
			k, err := s.scValToNative(ent.Key, ty.Map.KeyType)
			if err != nil {
				return nil, fmt.Errorf("map key: %w", err)
			}
			val, err := s.scValToNative(ent.Val, ty.Map.ValueType)
			if err != nil {
				return nil, fmt.Errorf("map value: %w", err)
			}
			ks, ok := k.(string)
			if !ok {
				return nil, invalidArgsf("map key decoded to non-string %T despite string-keyed type", k)
			}
			out[ks] = val
		}
		return out, nil

	case xdr.ScSpecTypeScSpecTypeTuple:
		if v.Type != xdr.ScValTypeScvVec {
			return nil, typeMismatch(ty.Type, v.Type)
		}
		if ty.Tuple == nil {
			return nil, invalidArgsf("tuple spec missing value types")
		}
		vec := v.MustVec()
		var items []xdr.ScVal
		if vec != nil {
			items = *vec
		}
		if len(items) != len(ty.Tuple.ValueTypes) {
			return nil, invalidArgsf("tuple arity mismatch: want %d, got %d",
				len(ty.Tuple.ValueTypes), len(items))
		}
		out := make([]any, len(items))
		for i, item := range items {
			ev, err := s.scValToNative(item, ty.Tuple.ValueTypes[i])
			if err != nil {
				return nil, fmt.Errorf("tuple[%d]: %w", i, err)
			}
			out[i] = ev
		}
		return out, nil

	case xdr.ScSpecTypeScSpecTypeUdt:
		if ty.Udt == nil {
			return nil, invalidArgsf("udt spec missing name")
		}
		return s.udtFromScVal(v, ty.Udt.Name)
	}

	return nil, invalidArgsf("unsupported ScSpecType: %s", ty.Type)
}

func (s *Spec) udtFromScVal(v xdr.ScVal, name string) (any, error) {
	entry, ok := s.lookup(name)
	if !ok {
		return nil, invalidArgsf("udt %q not found in spec", name)
	}
	switch entry.Kind {
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		return s.structFromScVal(v, entry.UdtStructV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		return enumFromScVal(v, entry.UdtEnumV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0:
		return errorEnumFromScVal(v, entry.UdtErrorEnumV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		return s.unionFromScVal(v, entry.UdtUnionV0)
	}
	return nil, invalidArgsf("udt %q has unsupported kind %s", name, entry.Kind)
}

func (s *Spec) unionFromScVal(v xdr.ScVal, udt *xdr.ScSpecUdtUnionV0) (any, error) {
	if udt == nil {
		return nil, invalidArgsf("union udt is nil")
	}
	if v.Type != xdr.ScValTypeScvVec {
		return nil, invalidArgsf("expected ScvVec for union %q, got %s", udt.Name, v.Type)
	}
	vec := v.MustVec()
	var items []xdr.ScVal
	if vec != nil {
		items = *vec
	}
	if len(items) == 0 {
		return nil, invalidArgsf("union %q has empty vec", udt.Name)
	}
	if items[0].Type != xdr.ScValTypeScvSymbol {
		return nil, invalidArgsf("union %q tag must be ScvSymbol, got %s", udt.Name, items[0].Type)
	}
	tag := string(*items[0].Sym)
	for _, c := range udt.Cases {
		switch c.Kind {
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0:
			if c.VoidCase == nil || c.VoidCase.Name != tag {
				continue
			}
			if len(items) != 1 {
				return nil, invalidArgsf("union %q void case %q has %d extra values",
					udt.Name, tag, len(items)-1)
			}
			return map[string]any{"tag": tag}, nil
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0:
			if c.TupleCase == nil || c.TupleCase.Name != tag {
				continue
			}
			types := c.TupleCase.Type
			if len(items)-1 != len(types) {
				return nil, invalidArgsf("union %q case %q expects %d values, got %d",
					udt.Name, tag, len(types), len(items)-1)
			}
			values := make([]any, len(types))
			for i, t := range types {
				val, err := s.scValToNative(items[i+1], t)
				if err != nil {
					return nil, fmt.Errorf("union %q case %q values[%d]: %w", udt.Name, tag, i, err)
				}
				values[i] = val
			}
			return map[string]any{"tag": tag, "values": values}, nil
		}
	}
	return nil, invalidArgsf("union %q has no case %q", udt.Name, tag)
}

func (s *Spec) structFromScVal(v xdr.ScVal, udt *xdr.ScSpecUdtStructV0) (any, error) {
	if udt == nil {
		return nil, invalidArgsf("struct udt is nil")
	}
	if v.Type != xdr.ScValTypeScvMap {
		return nil, invalidArgsf("expected ScvMap for struct %q, got %s", udt.Name, v.Type)
	}
	mp := v.MustMap()
	if mp == nil {
		return nil, invalidArgsf("struct %q has nil map", udt.Name)
	}
	out := make(map[string]any, len(udt.Fields))
	for _, field := range udt.Fields {
		var entVal *xdr.ScVal
		for i := range *mp {
			ent := &(*mp)[i]
			if ent.Key.Type == xdr.ScValTypeScvSymbol && string(*ent.Key.Sym) == field.Name {
				entVal = &ent.Val
				break
			}
		}
		if entVal == nil {
			return nil, invalidArgsf("struct %q missing field %q", udt.Name, field.Name)
		}
		fv, err := s.scValToNative(*entVal, field.Type)
		if err != nil {
			return nil, fmt.Errorf("struct %q field %q: %w", udt.Name, field.Name, err)
		}
		out[field.Name] = fv
	}
	return out, nil
}

func enumFromScVal(v xdr.ScVal, udt *xdr.ScSpecUdtEnumV0) (any, error) {
	if udt == nil {
		return nil, invalidArgsf("enum udt is nil")
	}
	if v.Type != xdr.ScValTypeScvU32 {
		return nil, invalidArgsf("expected ScvU32 for enum %q, got %s", udt.Name, v.Type)
	}
	val := uint32(v.MustU32())
	for _, c := range udt.Cases {
		if uint32(c.Value) == val {
			return c.Name, nil
		}
	}
	return nil, invalidArgsf("enum %q has no case with value %d", udt.Name, val)
}

func errorEnumFromScVal(v xdr.ScVal, udt *xdr.ScSpecUdtErrorEnumV0) (any, error) {
	if udt == nil {
		return nil, invalidArgsf("error-enum udt is nil")
	}
	if v.Type != xdr.ScValTypeScvError {
		return nil, invalidArgsf("expected ScvError for error-enum %q, got %s", udt.Name, v.Type)
	}
	se := v.MustError()
	if se.Type != xdr.ScErrorTypeSceContract || se.ContractCode == nil {
		return nil, invalidArgsf("error-enum %q expected contract-error, got %s", udt.Name, se.Type)
	}
	code := uint32(*se.ContractCode)
	for _, c := range udt.Cases {
		if uint32(c.Value) == code {
			return c.Name, nil
		}
	}
	return nil, invalidArgsf("error-enum %q has no case with value %d", udt.Name, code)
}

// ---------- helpers ----------

func (s *Spec) lookup(name string) (xdr.ScSpecEntry, bool) {
	if s == nil {
		return xdr.ScSpecEntry{}, false
	}
	i, ok := s.byName[name]
	if !ok {
		return xdr.ScSpecEntry{}, false
	}
	return s.entries[i], true
}

func invalidArgsf(format string, args ...any) error {
	return &Error{Kind: KindInvalidArgs, Details: fmt.Sprintf(format, args...)}
}

func typeMismatch(want xdr.ScSpecType, got xdr.ScValType) error {
	return invalidArgsf("type mismatch: expected %s, got %s", want, got)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

func toBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Array && rv.Type().Elem().Kind() == reflect.Uint8 {
		out := make([]byte, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = byte(rv.Index(i).Uint())
		}
		return out, nil
	}
	return nil, invalidArgsf("expected []byte for bytes, got %T", v)
}

func toSlice(v any) ([]any, error) {
	if a, ok := v.([]any); ok {
		return a, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, invalidArgsf("expected slice for vec/tuple, got %T", v)
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, nil
}

func toUint(v any, bits int) (uint64, error) {
	switch x := v.(type) {
	case uint:
		return uint64(x), nil
	case uint8:
		return uint64(x), nil
	case uint16:
		return uint64(x), nil
	case uint32:
		return uint64(x), nil
	case uint64:
		return checkUintBits(x, bits)
	case int:
		if x < 0 {
			return 0, invalidArgsf("negative value %d for unsigned", x)
		}
		return checkUintBits(uint64(x), bits)
	case int32:
		if x < 0 {
			return 0, invalidArgsf("negative value %d for unsigned", x)
		}
		return checkUintBits(uint64(x), bits)
	case int64:
		if x < 0 {
			return 0, invalidArgsf("negative value %d for unsigned", x)
		}
		return checkUintBits(uint64(x), bits)
	case xdr.Uint32:
		return uint64(x), nil
	case xdr.Uint64:
		return checkUintBits(uint64(x), bits)
	}
	return 0, invalidArgsf("expected unsigned integer, got %T", v)
}

func checkUintBits(x uint64, bits int) (uint64, error) {
	if bits == 64 {
		return x, nil
	}
	if x > (uint64(1)<<uint(bits))-1 {
		return 0, invalidArgsf("value %d overflows u%d", x, bits)
	}
	return x, nil
}

func toInt(v any, bits int) (int64, error) {
	switch x := v.(type) {
	case int:
		return checkIntBits(int64(x), bits)
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return checkIntBits(int64(x), bits)
	case int64:
		return checkIntBits(x, bits)
	case uint:
		return checkIntBits(int64(x), bits)
	case uint32:
		return checkIntBits(int64(x), bits)
	case uint64:
		if x > (1 << 63) {
			return 0, invalidArgsf("uint64 %d overflows i64", x)
		}
		return checkIntBits(int64(x), bits)
	case xdr.Int32:
		return int64(x), nil
	case xdr.Int64:
		return checkIntBits(int64(x), bits)
	}
	return 0, invalidArgsf("expected signed integer, got %T", v)
}

func checkIntBits(x int64, bits int) (int64, error) {
	if bits == 64 {
		return x, nil
	}
	max := int64(1)<<uint(bits-1) - 1
	min := -int64(1) << uint(bits-1)
	if x < min || x > max {
		return 0, invalidArgsf("value %d overflows i%d", x, bits)
	}
	return x, nil
}

func toBigInt(v any) (*big.Int, error) {
	switch x := v.(type) {
	case *big.Int:
		if x == nil {
			return nil, invalidArgsf("nil *big.Int")
		}
		return x, nil
	case big.Int:
		return &x, nil
	case int:
		return big.NewInt(int64(x)), nil
	case int32:
		return big.NewInt(int64(x)), nil
	case int64:
		return big.NewInt(x), nil
	case uint:
		return new(big.Int).SetUint64(uint64(x)), nil
	case uint32:
		return new(big.Int).SetUint64(uint64(x)), nil
	case uint64:
		return new(big.Int).SetUint64(x), nil
	}
	return nil, invalidArgsf("expected *big.Int or integer, got %T", v)
}

// sortMapEntries orders map entries deterministically: lexicographic by the
// canonical bytes of the encoded key ScVal. For symbol/string keys this is
// equivalent to lexicographic-by-string, matching xdr.ScvMap's behavior.
func sortMapEntries(entries []xdr.ScMapEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return scMapKeyLess(entries[i].Key, entries[j].Key)
	})
}

func scMapKeyLess(a, b xdr.ScVal) bool {
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	switch a.Type {
	case xdr.ScValTypeScvSymbol:
		return string(*a.Sym) < string(*b.Sym)
	case xdr.ScValTypeScvString:
		return string(*a.Str) < string(*b.Str)
	case xdr.ScValTypeScvU32:
		return uint32(*a.U32) < uint32(*b.U32)
	case xdr.ScValTypeScvI32:
		return int32(*a.I32) < int32(*b.I32)
	case xdr.ScValTypeScvU64:
		return uint64(*a.U64) < uint64(*b.U64)
	case xdr.ScValTypeScvI64:
		return int64(*a.I64) < int64(*b.I64)
	}
	// Fallback: encode both and compare bytes. Stable but slow; unreachable
	// for the canonical key types Soroban contracts use.
	ab, _ := a.MarshalBinary()
	bb, _ := b.MarshalBinary()
	return string(ab) < string(bb)
}

func isStringKeyedMap(ty xdr.ScSpecTypeDef) bool {
	switch ty.Type {
	case xdr.ScSpecTypeScSpecTypeSymbol, xdr.ScSpecTypeScSpecTypeString,
		xdr.ScSpecTypeScSpecTypeAddress, xdr.ScSpecTypeScSpecTypeMuxedAddress:
		return true
	}
	return false
}

// ---------- 128/256-bit big.Int <-> Parts ----------

var (
	maxU256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	maxI256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))
	minI256 = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 255))
	mask64M = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
)

func u256Parts(v *big.Int) (xdr.UInt256Parts, error) {
	if v == nil {
		return xdr.UInt256Parts{}, invalidArgsf("nil *big.Int for u256")
	}
	if v.Sign() < 0 || v.Cmp(maxU256) > 0 {
		return xdr.UInt256Parts{}, invalidArgsf("value out of range for u256")
	}
	u := new(big.Int).Set(v)
	hihi := new(big.Int).Rsh(u, 192)
	hilo := new(big.Int).And(new(big.Int).Rsh(u, 128), mask64M)
	lohi := new(big.Int).And(new(big.Int).Rsh(u, 64), mask64M)
	lolo := new(big.Int).And(u, mask64M)
	return xdr.UInt256Parts{
		HiHi: xdr.Uint64(hihi.Uint64()),
		HiLo: xdr.Uint64(hilo.Uint64()),
		LoHi: xdr.Uint64(lohi.Uint64()),
		LoLo: xdr.Uint64(lolo.Uint64()),
	}, nil
}

func i256Parts(v *big.Int) (xdr.Int256Parts, error) {
	if v == nil {
		return xdr.Int256Parts{}, invalidArgsf("nil *big.Int for i256")
	}
	if v.Cmp(minI256) < 0 || v.Cmp(maxI256) > 0 {
		return xdr.Int256Parts{}, invalidArgsf("value out of range for i256")
	}
	u := new(big.Int).Set(v)
	if u.Sign() < 0 {
		u.Add(u, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	hihi := new(big.Int).Rsh(u, 192)
	hilo := new(big.Int).And(new(big.Int).Rsh(u, 128), mask64M)
	lohi := new(big.Int).And(new(big.Int).Rsh(u, 64), mask64M)
	lolo := new(big.Int).And(u, mask64M)
	return xdr.Int256Parts{
		HiHi: xdr.Int64(int64(hihi.Uint64())),
		HiLo: xdr.Uint64(hilo.Uint64()),
		LoHi: xdr.Uint64(lohi.Uint64()),
		LoLo: xdr.Uint64(lolo.Uint64()),
	}, nil
}

func u128ToBigInt(p xdr.UInt128Parts) *big.Int {
	hi := new(big.Int).SetUint64(uint64(p.Hi))
	lo := new(big.Int).SetUint64(uint64(p.Lo))
	return new(big.Int).Add(new(big.Int).Lsh(hi, 64), lo)
}

func i128ToBigInt(p xdr.Int128Parts) *big.Int {
	hi := new(big.Int).SetInt64(int64(p.Hi))
	lo := new(big.Int).SetUint64(uint64(p.Lo))
	return new(big.Int).Add(new(big.Int).Lsh(hi, 64), lo)
}

func u256ToBigInt(p xdr.UInt256Parts) *big.Int {
	out := new(big.Int).SetUint64(uint64(p.HiHi))
	out.Lsh(out, 64)
	out.Add(out, new(big.Int).SetUint64(uint64(p.HiLo)))
	out.Lsh(out, 64)
	out.Add(out, new(big.Int).SetUint64(uint64(p.LoHi)))
	out.Lsh(out, 64)
	out.Add(out, new(big.Int).SetUint64(uint64(p.LoLo)))
	return out
}

func i256ToBigInt(p xdr.Int256Parts) *big.Int {
	// Treat HiHi as signed; the rest are unsigned big-endian parts.
	out := new(big.Int).SetInt64(int64(p.HiHi))
	if out.Sign() >= 0 {
		out.Lsh(out, 64)
		out.Add(out, new(big.Int).SetUint64(uint64(p.HiLo)))
		out.Lsh(out, 64)
		out.Add(out, new(big.Int).SetUint64(uint64(p.LoHi)))
		out.Lsh(out, 64)
		out.Add(out, new(big.Int).SetUint64(uint64(p.LoLo)))
		return out
	}
	// Negative: reconstruct as unsigned 256-bit, then subtract 2^256.
	u := new(big.Int).SetUint64(uint64(uint64(p.HiHi)))
	u.Lsh(u, 64)
	u.Add(u, new(big.Int).SetUint64(uint64(p.HiLo)))
	u.Lsh(u, 64)
	u.Add(u, new(big.Int).SetUint64(uint64(p.LoHi)))
	u.Lsh(u, 64)
	u.Add(u, new(big.Int).SetUint64(uint64(p.LoLo)))
	u.Sub(u, new(big.Int).Lsh(big.NewInt(1), 256))
	return u
}

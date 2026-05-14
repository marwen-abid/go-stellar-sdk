package xdr

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// One-liner constructors for ScVal values. These exist because every call
// site that builds Soroban host-function arguments today repeats the same
// 3–6 lines of pointer+enum boilerplate; see design doc §4.9.
//
// The Scv* functions never panic. Variants that take user-controlled strings
// or big.Ints validate their inputs and return an error on overflow / bad
// encoding rather than producing an ScVal that would fail later at XDR
// marshal time.

// ScvAddress builds an ScVal carrying an ScAddress decoded from a strkey.
// Accepts account (G...), contract (C...), and muxed-account (M...) strkeys.
func ScvAddress(strkeyAddr string) (ScVal, error) {
	addr, err := scAddressFromStrkey(strkeyAddr)
	if err != nil {
		return ScVal{}, err
	}
	return ScVal{Type: ScValTypeScvAddress, Address: &addr}, nil
}

// ScvI128 builds an ScVal of type SCV_I128 from a *big.Int.
// Returns an error if v is outside [-2^127, 2^127-1] or nil.
func ScvI128(v *big.Int) (ScVal, error) {
	if v == nil {
		return ScVal{}, fmt.Errorf("xdr: ScvI128: nil value")
	}
	parts, err := i128Parts(v)
	if err != nil {
		return ScVal{}, err
	}
	return ScVal{Type: ScValTypeScvI128, I128: &parts}, nil
}

// ScvU128 builds an ScVal of type SCV_U128 from a *big.Int.
// Returns an error if v is negative, nil, or >= 2^128.
func ScvU128(v *big.Int) (ScVal, error) {
	if v == nil {
		return ScVal{}, fmt.Errorf("xdr: ScvU128: nil value")
	}
	parts, err := u128Parts(v)
	if err != nil {
		return ScVal{}, err
	}
	return ScVal{Type: ScValTypeScvU128, U128: &parts}, nil
}

// ScvI128FromInt64 builds an SCV_I128 from a signed 64-bit value.
func ScvI128FromInt64(v int64) ScVal {
	hi := Int64(0)
	if v < 0 {
		hi = -1
	}
	parts := Int128Parts{Hi: hi, Lo: Uint64(uint64(v))}
	return ScVal{Type: ScValTypeScvI128, I128: &parts}
}

// ScvU128FromUint64 builds an SCV_U128 from an unsigned 64-bit value.
func ScvU128FromUint64(v uint64) ScVal {
	parts := UInt128Parts{Hi: 0, Lo: Uint64(v)}
	return ScVal{Type: ScValTypeScvU128, U128: &parts}
}

// ScvSymbol builds an SCV_SYMBOL after validating the input matches the
// host-defined symbol charset ([a-zA-Z0-9_], max 32 bytes).
func ScvSymbol(s string) (ScVal, error) {
	if err := validateScSymbol(s); err != nil {
		return ScVal{}, err
	}
	sym := ScSymbol(s)
	return ScVal{Type: ScValTypeScvSymbol, Sym: &sym}, nil
}

// ScvString builds an SCV_STRING. Strings are unrestricted byte sequences
// at the protocol level so no validation is performed.
func ScvString(s string) ScVal {
	str := ScString(s)
	return ScVal{Type: ScValTypeScvString, Str: &str}
}

// ScvBool builds an SCV_BOOL.
func ScvBool(b bool) ScVal {
	return ScVal{Type: ScValTypeScvBool, B: &b}
}

// ScvBytes builds an SCV_BYTES. The slice is referenced, not copied.
func ScvBytes(b []byte) ScVal {
	scb := ScBytes(b)
	return ScVal{Type: ScValTypeScvBytes, Bytes: &scb}
}

// ScvVec builds an SCV_VEC from a slice of ScVals.
func ScvVec(items ...ScVal) ScVal {
	vec := ScVec(items)
	pv := &vec
	return ScVal{Type: ScValTypeScvVec, Vec: &pv}
}

// ScvMap builds an SCV_MAP from a Go map with symbol keys. Entries are
// emitted in lexicographic order so callers get deterministic, canonical
// encoding regardless of map iteration order.
func ScvMap(kv map[string]ScVal) (ScVal, error) {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		if err := validateScSymbol(k); err != nil {
			return ScVal{}, fmt.Errorf("xdr: ScvMap: key %q: %w", k, err)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make(ScMap, len(keys))
	for i, k := range keys {
		sym := ScSymbol(k)
		entries[i] = ScMapEntry{
			Key: ScVal{Type: ScValTypeScvSymbol, Sym: &sym},
			Val: kv[k],
		}
	}
	pm := &entries
	return ScVal{Type: ScValTypeScvMap, Map: &pm}, nil
}

// scAddressFromStrkey converts a strkey (G..., C..., M...) into an
// ScAddress. T0.2 will promote this to public ScAddressFromStrkey.
func scAddressFromStrkey(s string) (ScAddress, error) {
	if raw, err := strkey.Decode(strkey.VersionByteAccountID, s); err == nil {
		var pub Uint256
		copy(pub[:], raw)
		return ScAddress{
			Type: ScAddressTypeScAddressTypeAccount,
			AccountId: &AccountId{
				Type:    PublicKeyTypePublicKeyTypeEd25519,
				Ed25519: &pub,
			},
		}, nil
	}
	if raw, err := strkey.Decode(strkey.VersionByteContract, s); err == nil {
		var cid ContractId
		copy(cid[:], raw)
		return ScAddress{
			Type:       ScAddressTypeScAddressTypeContract,
			ContractId: &cid,
		}, nil
	}
	if raw, err := strkey.Decode(strkey.VersionByteMuxedAccount, s); err == nil {
		// Muxed strkey payload is 32-byte ed25519 followed by 8-byte big-endian id.
		if len(raw) != 40 {
			return ScAddress{}, fmt.Errorf("xdr: ScAddress: muxed payload length %d, expected 40", len(raw))
		}
		var pub Uint256
		copy(pub[:], raw[:32])
		id := uint64(raw[32])<<56 |
			uint64(raw[33])<<48 |
			uint64(raw[34])<<40 |
			uint64(raw[35])<<32 |
			uint64(raw[36])<<24 |
			uint64(raw[37])<<16 |
			uint64(raw[38])<<8 |
			uint64(raw[39])
		return ScAddress{
			Type: ScAddressTypeScAddressTypeMuxedAccount,
			MuxedAccount: &MuxedEd25519Account{
				Id:      Uint64(id),
				Ed25519: pub,
			},
		}, nil
	}
	return ScAddress{}, fmt.Errorf("xdr: ScAddress: %q is not a G, C, or M strkey", s)
}

func validateScSymbol(s string) error {
	if len(s) > 32 {
		return fmt.Errorf("xdr: ScSymbol: length %d exceeds max 32", len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_'
		if !ok {
			return fmt.Errorf("xdr: ScSymbol: invalid character %q at index %d", c, i)
		}
	}
	return nil
}

var (
	maxI128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 127), big.NewInt(1)) // 2^127 - 1
	minI128 = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 127))                // -2^127
	maxU128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)) // 2^128 - 1
	mask64  = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))  // 2^64 - 1
)

func i128Parts(v *big.Int) (Int128Parts, error) {
	if v.Cmp(minI128) < 0 || v.Cmp(maxI128) > 0 {
		return Int128Parts{}, fmt.Errorf("xdr: ScvI128: value out of range [-2^127, 2^127-1]")
	}
	// Use two's-complement representation: take v mod 2^128, then split.
	u := new(big.Int).Set(v)
	if u.Sign() < 0 {
		u.Add(u, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	lo := new(big.Int).And(u, mask64)
	hi := new(big.Int).Rsh(u, 64)
	return Int128Parts{
		Hi: Int64(int64(hi.Uint64())),
		Lo: Uint64(lo.Uint64()),
	}, nil
}

func u128Parts(v *big.Int) (UInt128Parts, error) {
	if v.Sign() < 0 || v.Cmp(maxU128) > 0 {
		return UInt128Parts{}, fmt.Errorf("xdr: ScvU128: value out of range [0, 2^128)")
	}
	lo := new(big.Int).And(v, mask64)
	hi := new(big.Int).Rsh(v, 64)
	return UInt128Parts{
		Hi: Uint64(hi.Uint64()),
		Lo: Uint64(lo.Uint64()),
	}, nil
}

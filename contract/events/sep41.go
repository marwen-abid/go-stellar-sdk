package events

import (
	"errors"
	"math/big"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Generic SEP-41 event decoders.
//
// SEP-41 is the Soroban token-interface standard. Any contract that emits
// SEP-41 events — including but not limited to the native Stellar Asset
// Contract (SAC) — produces "transfer", "mint", "burn", and "clawback"
// topics with well-known shapes. These decoders parse those events without
// performing any asset-id / contract-id integrity validation; that
// stronger SAC-only check lives at events.ValidateSACEvent (T6.5).
//
// The decoders are wired into Decode as an automatic fallback: when no
// contract-specific decoder is registered for a SEP-41 topic, Decode
// dispatches to the matching ParseSEP41* function below. Consumers who want
// to override the generic behavior for a specific contract can
// RegisterDecoder against that contract's strkey id.

// SEP41Topics is the set of leading event topics recognized as SEP-41
// standard events. Used by Decode to decide whether to fall back to the
// generic parser when no contract-specific decoder is registered.
var SEP41Topics = map[string]struct{}{
	"transfer": {},
	"mint":     {},
	"burn":     {},
	"clawback": {},
}

// Transfer is the typed form of a SEP-41 "transfer" event.
//
// Asset is populated only when the event carries the SAC-style trailing
// asset topic in canonical SEP-11 form ("native" or "<code>:<issuer>");
// generic SEP-41 contracts emit no asset topic and Asset is the empty
// string.
type Transfer struct {
	ContractID xdr.ContractId
	From       string
	To         string
	Asset      string
	Amount     *big.Int
}

// Mint is the typed form of a SEP-41 "mint" event. Admin is the address
// that authorized the mint; To is the recipient. Asset is populated only
// for SAC-style events (see Transfer.Asset).
type Mint struct {
	ContractID xdr.ContractId
	Admin      string
	To         string
	Asset      string
	Amount     *big.Int
}

// Burn is the typed form of a SEP-41 "burn" event.
type Burn struct {
	ContractID xdr.ContractId
	From       string
	Amount     *big.Int
}

// Clawback is the typed form of a SEP-41 "clawback" event.
type Clawback struct {
	ContractID xdr.ContractId
	Admin      string
	From       string
	Amount     *big.Int
}

// ErrNotSEP41Event is returned when a ContractEvent does not match the
// expected SEP-41 shape for the topic being parsed.
var ErrNotSEP41Event = errors.New("events: event is not a valid SEP-41 event")

// ParseSEP41Transfer decodes a SEP-41 "transfer" event.
//
// Shape:
//
//	topics = [Sym("transfer"), Address(from), Address(to)]
//	data   = i128 amount
//
// SAC also emits a trailing canonical-asset topic; when present, the
// returned Transfer.Asset is set to it. No asset-id / contract-id
// integrity check is performed.
func ParseSEP41Transfer(e xdr.ContractEvent) (*Transfer, error) {
	topics, data, ok := sep41Body(e, "transfer", 3, 4)
	if !ok {
		return nil, ErrNotSEP41Event
	}
	from, err := topicAddress(topics, 1)
	if err != nil {
		return nil, err
	}
	to, err := topicAddress(topics, 2)
	if err != nil {
		return nil, err
	}
	amount, err := i128Amount(data)
	if err != nil {
		return nil, err
	}
	out := &Transfer{
		ContractID: *e.ContractId,
		From:       from,
		To:         to,
		Amount:     amount,
	}
	if len(topics) == 4 {
		out.Asset = topicString(topics, 3)
	}
	return out, nil
}

// ParseSEP41Mint decodes a SEP-41 "mint" event.
//
// Shape:
//
//	topics = [Sym("mint"), Address(admin), Address(to)]
//	data   = i128 amount
//
// SAC also emits a trailing canonical-asset topic; when present, Mint.Asset
// is set to it.
func ParseSEP41Mint(e xdr.ContractEvent) (*Mint, error) {
	topics, data, ok := sep41Body(e, "mint", 3, 4)
	if !ok {
		return nil, ErrNotSEP41Event
	}
	admin, err := topicAddress(topics, 1)
	if err != nil {
		return nil, err
	}
	to, err := topicAddress(topics, 2)
	if err != nil {
		return nil, err
	}
	amount, err := i128Amount(data)
	if err != nil {
		return nil, err
	}
	out := &Mint{
		ContractID: *e.ContractId,
		Admin:      admin,
		To:         to,
		Amount:     amount,
	}
	if len(topics) == 4 {
		out.Asset = topicString(topics, 3)
	}
	return out, nil
}

// ParseSEP41Burn decodes a SEP-41 "burn" event.
//
// Shape:
//
//	topics = [Sym("burn"), Address(from)]
//	data   = i128 amount
//
// SAC also emits a trailing canonical-asset topic; that topic is accepted
// but discarded (it has no field on Burn per the design doc).
func ParseSEP41Burn(e xdr.ContractEvent) (*Burn, error) {
	topics, data, ok := sep41Body(e, "burn", 2, 3)
	if !ok {
		return nil, ErrNotSEP41Event
	}
	from, err := topicAddress(topics, 1)
	if err != nil {
		return nil, err
	}
	amount, err := i128Amount(data)
	if err != nil {
		return nil, err
	}
	return &Burn{
		ContractID: *e.ContractId,
		From:       from,
		Amount:     amount,
	}, nil
}

// ParseSEP41Clawback decodes a SEP-41 "clawback" event.
//
// Shape:
//
//	topics = [Sym("clawback"), Address(admin), Address(from)]
//	data   = i128 amount
//
// SAC also emits a trailing canonical-asset topic; that topic is accepted
// but discarded (it has no field on Clawback per the design doc).
func ParseSEP41Clawback(e xdr.ContractEvent) (*Clawback, error) {
	topics, data, ok := sep41Body(e, "clawback", 3, 4)
	if !ok {
		return nil, ErrNotSEP41Event
	}
	admin, err := topicAddress(topics, 1)
	if err != nil {
		return nil, err
	}
	from, err := topicAddress(topics, 2)
	if err != nil {
		return nil, err
	}
	amount, err := i128Amount(data)
	if err != nil {
		return nil, err
	}
	return &Clawback{
		ContractID: *e.ContractId,
		Admin:      admin,
		From:       from,
		Amount:     amount,
	}, nil
}

// decodeSEP41 dispatches to the matching ParseSEP41* parser based on the
// leading topic symbol of e. Called from registry.Decode as the generic
// fallback when no contract-specific decoder is registered.
func decodeSEP41(e xdr.ContractEvent) (any, error) {
	_, topic, err := eventKey(e)
	if err != nil {
		return nil, err
	}
	switch topic {
	case "transfer":
		return ParseSEP41Transfer(e)
	case "mint":
		return ParseSEP41Mint(e)
	case "burn":
		return ParseSEP41Burn(e)
	case "clawback":
		return ParseSEP41Clawback(e)
	default:
		return nil, ErrNoDecoder
	}
}

// sep41Body extracts (topics, data) from a ContractEvent and verifies that
// the leading topic is the expected symbol and that the topic count is one
// of the allowed values. Returns ok=false on any structural mismatch.
func sep41Body(e xdr.ContractEvent, want string, allowedCounts ...int) (xdr.ScVec, xdr.ScVal, bool) {
	if e.Type != xdr.ContractEventTypeContract || e.ContractId == nil || e.Body.V != 0 || e.Body.V0 == nil {
		return nil, xdr.ScVal{}, false
	}
	topics := e.Body.V0.Topics
	if len(topics) == 0 {
		return nil, xdr.ScVal{}, false
	}
	sym, ok := topics[0].GetSym()
	if !ok || string(sym) != want {
		return nil, xdr.ScVal{}, false
	}
	matched := false
	for _, n := range allowedCounts {
		if len(topics) == n {
			matched = true
			break
		}
	}
	if !matched {
		return nil, xdr.ScVal{}, false
	}
	return topics, e.Body.V0.Data, true
}

// topicAddress reads topics[i] as an ScAddress and returns its strkey
// rendering.
func topicAddress(topics xdr.ScVec, i int) (string, error) {
	addr, ok := topics[i].GetAddress()
	if !ok {
		return "", ErrNotSEP41Event
	}
	s, err := addr.String()
	if err != nil {
		return "", ErrNotSEP41Event
	}
	return s, nil
}

// topicString reads topics[i] as an ScString, returning the empty string
// if the topic is not a string (callers only invoke this on optional
// SAC-style topics).
func topicString(topics xdr.ScVec, i int) string {
	s, ok := topics[i].GetStr()
	if !ok {
		return ""
	}
	return string(s)
}

// i128Amount converts an i128 ScVal into a *big.Int.
func i128Amount(v xdr.ScVal) (*big.Int, error) {
	parts, ok := v.GetI128()
	if !ok {
		return nil, ErrNotSEP41Event
	}
	hi := new(big.Int).SetInt64(int64(parts.Hi))
	lo := new(big.Int).SetUint64(uint64(parts.Lo))
	out := new(big.Int).Lsh(hi, 64)
	return out.Or(out, lo), nil
}

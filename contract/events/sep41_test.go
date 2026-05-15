package events

import (
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sep41Account returns a fresh "G..." account strkey for use as a SEP-41
// event participant.
func sep41Account(t *testing.T) string {
	t.Helper()
	return keypair.MustRandom().Address()
}

// sep41Contract returns a deterministic "C..." contract strkey + the raw
// 32-byte id behind it.
func sep41Contract(t *testing.T, seed byte) (string, xdr.ContractId) {
	t.Helper()
	var raw [32]byte
	for i := range raw {
		raw[i] = seed
	}
	s, err := strkey.Encode(strkey.VersionByteContract, raw[:])
	require.NoError(t, err)
	return s, xdr.ContractId(raw)
}

// sep41Event builds a ContractEvent with the given contract id, topics,
// and i128 data.
func sep41Event(cid xdr.ContractId, topics xdr.ScVec, amount *big.Int) xdr.ContractEvent {
	c := cid
	return xdr.ContractEvent{
		Type:       xdr.ContractEventTypeContract,
		ContractId: &c,
		Body: xdr.ContractEventBody{
			V: 0,
			V0: &xdr.ContractEventV0{
				Topics: topics,
				Data:   makeBigAmount(amount),
			},
		},
	}
}

func TestParseSEP41Transfer_BareSEP41Shape(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x10)
	from := sep41Account(t)
	to := sep41Account(t)
	amount := big.NewInt(12345)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("transfer"),
		makeAddress(from),
		makeAddress(to),
	}, amount)

	got, err := ParseSEP41Transfer(evt)
	require.NoError(t, err)
	assert.Equal(t, cid, got.ContractID)
	assert.Equal(t, from, got.From)
	assert.Equal(t, to, got.To)
	assert.Equal(t, "", got.Asset)
	assert.Equal(t, 0, got.Amount.Cmp(amount))
}

func TestParseSEP41Transfer_SACShape(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x11)
	from := sep41Account(t)
	to := sep41Account(t)
	amount := big.NewInt(7)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("transfer"),
		makeAddress(from),
		makeAddress(to),
		makeAsset(xdr.MustNewNativeAsset()),
	}, amount)

	got, err := ParseSEP41Transfer(evt)
	require.NoError(t, err)
	assert.Equal(t, "native", got.Asset)
	assert.Equal(t, from, got.From)
	assert.Equal(t, to, got.To)
}

func TestParseSEP41Mint(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x12)
	admin := sep41Account(t)
	to := sep41Account(t)
	amount := big.NewInt(99)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("mint"),
		makeAddress(admin),
		makeAddress(to),
	}, amount)

	got, err := ParseSEP41Mint(evt)
	require.NoError(t, err)
	assert.Equal(t, cid, got.ContractID)
	assert.Equal(t, admin, got.Admin)
	assert.Equal(t, to, got.To)
	assert.Equal(t, 0, got.Amount.Cmp(amount))
}

func TestParseSEP41Burn(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x13)
	from := sep41Account(t)
	amount := big.NewInt(42)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("burn"),
		makeAddress(from),
	}, amount)

	got, err := ParseSEP41Burn(evt)
	require.NoError(t, err)
	assert.Equal(t, cid, got.ContractID)
	assert.Equal(t, from, got.From)
	assert.Equal(t, 0, got.Amount.Cmp(amount))
}

func TestParseSEP41Burn_AcceptsSACTopic(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x14)
	from := sep41Account(t)
	amount := big.NewInt(1)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("burn"),
		makeAddress(from),
		makeAsset(xdr.MustNewNativeAsset()),
	}, amount)

	got, err := ParseSEP41Burn(evt)
	require.NoError(t, err)
	assert.Equal(t, from, got.From)
}

func TestParseSEP41Clawback(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x15)
	admin := sep41Account(t)
	from := sep41Account(t)
	amount := big.NewInt(500)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("clawback"),
		makeAddress(admin),
		makeAddress(from),
	}, amount)

	got, err := ParseSEP41Clawback(evt)
	require.NoError(t, err)
	assert.Equal(t, cid, got.ContractID)
	assert.Equal(t, admin, got.Admin)
	assert.Equal(t, from, got.From)
	assert.Equal(t, 0, got.Amount.Cmp(amount))
}

func TestParseSEP41_BadShape(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x16)
	from := sep41Account(t)

	t.Run("wrong topic count", func(t *testing.T) {
		evt := sep41Event(cid, xdr.ScVec{
			makeSymbol("transfer"),
			makeAddress(from),
			// missing 'to'
		}, big.NewInt(1))
		_, err := ParseSEP41Transfer(evt)
		assert.ErrorIs(t, err, ErrNotSEP41Event)
	})

	t.Run("wrong leading symbol", func(t *testing.T) {
		evt := sep41Event(cid, xdr.ScVec{
			makeSymbol("approve"),
			makeAddress(from),
			makeAddress(from),
		}, big.NewInt(1))
		_, err := ParseSEP41Transfer(evt)
		assert.ErrorIs(t, err, ErrNotSEP41Event)
	})

	t.Run("non-address topic", func(t *testing.T) {
		evt := sep41Event(cid, xdr.ScVec{
			makeSymbol("transfer"),
			makeAddress(from),
			makeSymbol("not_an_address"),
		}, big.NewInt(1))
		_, err := ParseSEP41Transfer(evt)
		assert.ErrorIs(t, err, ErrNotSEP41Event)
	})

	t.Run("non-i128 data", func(t *testing.T) {
		c := cid
		evt := xdr.ContractEvent{
			Type:       xdr.ContractEventTypeContract,
			ContractId: &c,
			Body: xdr.ContractEventBody{
				V: 0,
				V0: &xdr.ContractEventV0{
					Topics: xdr.ScVec{
						makeSymbol("transfer"),
						makeAddress(from),
						makeAddress(from),
					},
					Data: makeSymbol("oops"),
				},
			},
		}
		_, err := ParseSEP41Transfer(evt)
		assert.ErrorIs(t, err, ErrNotSEP41Event)
	})
}

func TestDecode_SEP41Fallback(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x17)
	from := sep41Account(t)
	to := sep41Account(t)
	amount := big.NewInt(2024)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("transfer"),
		makeAddress(from),
		makeAddress(to),
	}, amount)

	got, err := Decode(evt)
	require.NoError(t, err)

	tr, ok := got.(*Transfer)
	require.True(t, ok, "Decode should return *Transfer for a SEP-41 transfer event, got %T", got)
	assert.Equal(t, from, tr.From)
	assert.Equal(t, to, tr.To)
	assert.Equal(t, 0, tr.Amount.Cmp(amount))
}

func TestDecode_PerContractDecoderWinsOverSEP41Fallback(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	cidStr, cid := sep41Contract(t, 0x18)
	from := sep41Account(t)
	to := sep41Account(t)

	called := false
	RegisterDecoder(cidStr, "transfer", decoderFunc(func(xdr.ContractEvent) (any, error) {
		called = true
		return "custom", nil
	}))

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("transfer"),
		makeAddress(from),
		makeAddress(to),
	}, big.NewInt(1))

	out, err := Decode(evt)
	require.NoError(t, err)
	assert.True(t, called, "registered per-contract decoder should be invoked instead of the SEP-41 fallback")
	assert.Equal(t, "custom", out)
}

func TestDecode_NonSEP41TopicStillErrNoDecoder(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	_, cid := sep41Contract(t, 0x19)
	from := sep41Account(t)

	evt := sep41Event(cid, xdr.ScVec{
		makeSymbol("approve"),
		makeAddress(from),
		makeAddress(from),
	}, big.NewInt(1))

	_, err := Decode(evt)
	assert.ErrorIs(t, err, ErrNoDecoder)
}

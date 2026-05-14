package xdr

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/strkey"
)

func TestScAddressFromStrkey_Account(t *testing.T) {
	addrStr := strkeyForTest(t, strkey.VersionByteAccountID, 0x40)
	addr, err := ScAddressFromStrkey(addrStr)
	require.NoError(t, err)
	require.Equal(t, ScAddressTypeScAddressTypeAccount, addr.Type)
	got, err := addr.String()
	require.NoError(t, err)
	require.Equal(t, addrStr, got)
}

func TestScAddressFromStrkey_Contract(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	cstr, err := strkey.Encode(strkey.VersionByteContract, raw)
	require.NoError(t, err)

	addr, err := ScAddressFromStrkey(cstr)
	require.NoError(t, err)
	require.Equal(t, ScAddressTypeScAddressTypeContract, addr.Type)
	got, err := addr.String()
	require.NoError(t, err)
	require.Equal(t, cstr, got)
}

func TestScAddressFromStrkey_Muxed(t *testing.T) {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(0x20 + i)
	}
	id := uint64(0x0123456789ABCDEF)
	payload := make([]byte, 40)
	copy(payload[:32], pub)
	for i := 0; i < 8; i++ {
		payload[32+i] = byte(id >> (56 - 8*i))
	}
	mstr, err := strkey.Encode(strkey.VersionByteMuxedAccount, payload)
	require.NoError(t, err)

	addr, err := ScAddressFromStrkey(mstr)
	require.NoError(t, err)
	require.Equal(t, ScAddressTypeScAddressTypeMuxedAccount, addr.Type)
	require.Equal(t, Uint64(id), addr.MuxedAccount.Id)
	require.Equal(t, pub, addr.MuxedAccount.Ed25519[:])
}

func TestScAddressFromStrkey_Invalid(t *testing.T) {
	_, err := ScAddressFromStrkey("not-a-strkey")
	require.Error(t, err)

	// Seed strkey (S...) decodes cleanly under VersionByteSeed but is not a
	// supported ScAddress kind, so we expect an error.
	seedStr := strkeyForTest(t, strkey.VersionByteSeed, 0x70)
	_, err = ScAddressFromStrkey(seedStr)
	require.Error(t, err)

	_, err = ScAddressFromStrkey("")
	require.Error(t, err)
}

func TestMustScAddressFromStrkey_Success(t *testing.T) {
	addrStr := strkeyForTest(t, strkey.VersionByteAccountID, 0x10)
	addr := MustScAddressFromStrkey(addrStr)
	require.Equal(t, ScAddressTypeScAddressTypeAccount, addr.Type)
}

func TestMustScAddressFromStrkey_PanicsOnInvalid(t *testing.T) {
	require.Panics(t, func() {
		MustScAddressFromStrkey("definitely-not-a-strkey")
	})
}

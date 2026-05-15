package contract

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/hash"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- Signer stub -------------------------------------------------

// recordingSigner is a Signer that captures every preimage it was asked
// to sign, lets the test choose the returned signature/error, and counts
// envelope-Sign calls.
type recordingSigner struct {
	addr     string
	hint     [4]byte
	pubKey   []byte // 32-byte ed25519 pubkey
	preimage []xdr.HashIdPreimage
	resp     xdr.Signature
	respErr  error
	txCalls  int
	txErr    error
}

func newRecordingSigner(t *testing.T, addr string) *recordingSigner {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteAccountID, addr)
	require.NoError(t, err)
	require.Len(t, raw, ed25519.PublicKeySize)
	var h [4]byte
	copy(h[:], raw[28:])
	// Default to a 64-byte sentinel signature; tests can override.
	sig := make([]byte, 64)
	for i := range sig {
		sig[i] = 0xAB
	}
	return &recordingSigner{addr: addr, hint: h, pubKey: raw, resp: xdr.Signature(sig)}
}

func (s *recordingSigner) Address() string { return s.addr }

func (s *recordingSigner) SignTransaction(_ string, tx *txnbuild.Transaction) (*txnbuild.Transaction, error) {
	s.txCalls++
	if s.txErr != nil {
		return nil, s.txErr
	}
	sig := xdr.DecoratedSignature{
		Hint:      xdr.SignatureHint(s.hint),
		Signature: make([]byte, 64),
	}
	for i := range sig.Signature {
		sig.Signature[i] = 0xCD
	}
	out, err := tx.AddSignatureDecorated(sig)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *recordingSigner) SignAuthEntryPreimage(_ string, preimage xdr.HashIdPreimage) (xdr.Signature, error) {
	s.preimage = append(s.preimage, preimage)
	if s.respErr != nil {
		return nil, s.respErr
	}
	return s.resp, nil
}

// ---------- assembly fixture --------------------------------------------

// fixtureSimulated returns an AssembledTransaction in the post-Simulate
// state, with auth entries supplied by the test and a deterministic
// source account.
func fixtureSimulated(t *testing.T, entries []xdr.SorobanAuthorizationEntry, footprintRW []xdr.LedgerKey) (*AssembledTransaction, *keypair.Full) {
	t.Helper()
	rpc := &fakeSimulator{}
	srcKP := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)
	op := newTestInvokeOp(t, srcKP.Address())

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               rpc,
		NetworkPassphrase: network.TestNetworkPassphrase,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     &acct,
		Op:                op,
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	require.NoError(t, err)

	// Place the auth entries and footprint directly on the op + cached
	// slice so we don't have to round-trip through the simulator stub.
	op.Auth = append([]xdr.SorobanAuthorizationEntry(nil), entries...)
	op.Ext = xdr.TransactionExt{
		V: 1,
		SorobanData: &xdr.SorobanTransactionData{
			Resources: xdr.SorobanResources{
				Footprint: xdr.LedgerFootprint{
					ReadOnly:  nil,
					ReadWrite: footprintRW,
				},
			},
		},
	}
	rebuilt, err := buildTx(&acct, op, txnbuild.MinBaseFee, nil, txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()})
	require.NoError(t, err)
	at.Built = rebuilt
	at.AuthEntries = append([]xdr.SorobanAuthorizationEntry(nil), entries...)
	at.Simulation = &protocol.SimulateTransactionResponse{} // mark as simulated
	return at, srcKP
}

// addressEntry builds a SorobanAuthorizationEntry credentialed by addr
// with the given nonce and (initially void) signature.
func addressEntry(t *testing.T, addr string, nonce int64) xdr.SorobanAuthorizationEntry {
	t.Helper()
	scAddr, err := xdr.ScAddressFromStrkey(addr)
	require.NoError(t, err)
	var cid xdr.ContractId
	for i := range cid {
		cid[i] = byte(i + 7)
	}
	return xdr.SorobanAuthorizationEntry{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsAddress,
			Address: &xdr.SorobanAddressCredentials{
				Address:                   scAddr,
				Nonce:                     xdr.Int64(nonce),
				SignatureExpirationLedger: 0,
				Signature:                 xdr.ScVal{Type: xdr.ScValTypeScvVoid},
			},
		},
		RootInvocation: xdr.SorobanAuthorizedInvocation{
			Function: xdr.SorobanAuthorizedFunction{
				Type: xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
				ContractFn: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &cid,
					},
					FunctionName: "noop",
				},
			},
		},
	}
}

// sourceAccountEntry returns an entry credentialed by SourceAccount.
func sourceAccountEntry() xdr.SorobanAuthorizationEntry {
	return xdr.SorobanAuthorizationEntry{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
	}
}

// ---------- IsReadCall --------------------------------------------------

func TestIsReadCall_TrueWhenNoAuthAndNoWrites(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	assert.True(t, at.IsReadCall())
}

func TestIsReadCall_FalseWhenAddressAuthPresent(t *testing.T) {
	authKP := keypair.MustRandom()
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{
		addressEntry(t, authKP.Address(), 1),
	}, nil)
	assert.False(t, at.IsReadCall())
}

func TestIsReadCall_FalseWhenSourceAuthButWritesPresent(t *testing.T) {
	rwKey := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(keypair.MustRandom().Address())},
	}
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{sourceAccountEntry()}, []xdr.LedgerKey{rwKey})
	assert.False(t, at.IsReadCall())
}

func TestIsReadCall_FalseBeforeSimulate(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	at.Simulation = nil
	assert.False(t, at.IsReadCall())
}

// ---------- NeedsNonInvokerSigningBy ------------------------------------

func TestNeedsNonInvokerSigningBy_DedupesAndSkipsSourceAccount(t *testing.T) {
	a := keypair.MustRandom().Address()
	b := keypair.MustRandom().Address()
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{
		sourceAccountEntry(),
		addressEntry(t, a, 1),
		addressEntry(t, b, 2),
		addressEntry(t, a, 3), // duplicate address, different nonce
	}, nil)

	got, err := at.NeedsNonInvokerSigningBy(false)
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, got)
}

func TestNeedsNonInvokerSigningBy_SkipsAlreadySignedByDefault(t *testing.T) {
	a := keypair.MustRandom().Address()
	signed := addressEntry(t, a, 1)
	signed.Credentials.Address.Signature = xdr.ScvBytes([]byte{1, 2, 3})

	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{signed}, nil)

	got, err := at.NeedsNonInvokerSigningBy(false)
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = at.NeedsNonInvokerSigningBy(true)
	require.NoError(t, err)
	assert.Equal(t, []string{a}, got)
}

func TestNeedsNonInvokerSigningBy_NotYetSimulated(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	at.Simulation = nil
	_, err := at.NeedsNonInvokerSigningBy(false)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindNotYetSimulated, e.Kind)
}

// ---------- SignAuthEntries ---------------------------------------------

func TestSignAuthEntries_PopulatesMatchingEntriesOnly(t *testing.T) {
	signerKP := keypair.MustRandom()
	otherKP := keypair.MustRandom()
	matching := addressEntry(t, signerKP.Address(), 100)
	other := addressEntry(t, otherKP.Address(), 200)
	src := sourceAccountEntry()
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{src, matching, other}, nil)

	sig := newRecordingSigner(t, signerKP.Address())
	require.NoError(t, at.SignAuthEntries(context.Background(), sig, 1234))

	// Only the matching entry's preimage was requested.
	require.Len(t, sig.preimage, 1)
	pre := sig.preimage[0]
	assert.Equal(t, xdr.EnvelopeTypeEnvelopeTypeSorobanAuthorization, pre.Type)
	require.NotNil(t, pre.SorobanAuthorization)
	assert.Equal(t, xdr.Int64(100), pre.SorobanAuthorization.Nonce)
	assert.Equal(t, xdr.Uint32(1234), pre.SorobanAuthorization.SignatureExpirationLedger)
	assert.Equal(t, xdr.Hash(network.ID(network.TestNetworkPassphrase)), pre.SorobanAuthorization.NetworkId)

	// Matching entry now carries a non-void signature ScVal and expiration.
	got := at.AuthEntries[1]
	require.Equal(t, xdr.SorobanCredentialsTypeSorobanCredentialsAddress, got.Credentials.Type)
	require.NotNil(t, got.Credentials.Address)
	assert.Equal(t, xdr.Uint32(1234), got.Credentials.Address.SignatureExpirationLedger)
	require.NotEqual(t, xdr.ScValTypeScvVoid, got.Credentials.Address.Signature.Type)

	// Source-account entry left untouched.
	assert.Equal(t, src, at.AuthEntries[0])
	// Other-address entry left unsigned.
	assert.Equal(t, xdr.ScValTypeScvVoid, at.AuthEntries[2].Credentials.Address.Signature.Type)
	assert.Equal(t, xdr.Uint32(0), at.AuthEntries[2].Credentials.Address.SignatureExpirationLedger)

	// op.Auth was updated to mirror the cached slice.
	require.Equal(t, at.AuthEntries, at.op.Auth)
}

func TestSignAuthEntries_SignatureScValIsCanonicalMap(t *testing.T) {
	signerKP := keypair.MustRandom()
	entry := addressEntry(t, signerKP.Address(), 1)
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{entry}, nil)

	sig := newRecordingSigner(t, signerKP.Address())
	require.NoError(t, at.SignAuthEntries(context.Background(), sig, 50))

	scval := at.AuthEntries[0].Credentials.Address.Signature
	require.Equal(t, xdr.ScValTypeScvVec, scval.Type)
	vec := *scval.MustVec()
	require.Len(t, vec, 1)
	m := vec[0]
	require.Equal(t, xdr.ScValTypeScvMap, m.Type)
	mapEntries := *m.MustMap()
	require.Len(t, mapEntries, 2)
	// Canonical lex order: public_key < signature.
	assert.Equal(t, "public_key", string(*mapEntries[0].Key.Sym))
	assert.Equal(t, "signature", string(*mapEntries[1].Key.Sym))
	pub := *mapEntries[0].Val.Bytes
	sigBytes := *mapEntries[1].Val.Bytes
	assert.Len(t, pub, 32)
	assert.Len(t, sigBytes, 64)
	// Pubkey matches the strkey decode of the signer.
	want, err := strkey.Decode(strkey.VersionByteAccountID, signerKP.Address())
	require.NoError(t, err)
	assert.Equal(t, want, []byte(pub))
}

func TestSignAuthEntries_PreimageHashMatchesSignerInput(t *testing.T) {
	// End-to-end: a real KeypairSigner signs an entry; we recompute the
	// digest from the preimage SignAuthEntries built and verify it.
	signerKP := keypair.MustRandom()
	entry := addressEntry(t, signerKP.Address(), 7)
	at, _ := fixtureSimulated(t, []xdr.SorobanAuthorizationEntry{entry}, nil)

	require.NoError(t, at.SignAuthEntries(context.Background(), KeypairSigner(signerKP), 99))

	signedEntry := at.AuthEntries[0]
	scval := signedEntry.Credentials.Address.Signature
	require.Equal(t, xdr.ScValTypeScvVec, scval.Type)

	// Reconstruct the preimage from the (now-mutated) entry; the digest
	// of that preimage's marshalled XDR must verify against the embedded
	// signature using the signer's public key.
	preimage := xdr.HashIdPreimage{
		Type: xdr.EnvelopeTypeEnvelopeTypeSorobanAuthorization,
		SorobanAuthorization: &xdr.HashIdPreimageSorobanAuthorization{
			NetworkId:                 xdr.Hash(network.ID(network.TestNetworkPassphrase)),
			Nonce:                     signedEntry.Credentials.Address.Nonce,
			SignatureExpirationLedger: signedEntry.Credentials.Address.SignatureExpirationLedger,
			Invocation:                signedEntry.RootInvocation,
		},
	}
	raw, err := preimage.MarshalBinary()
	require.NoError(t, err)
	digest := hash.Hash(raw)

	vec := *scval.MustVec()
	m := vec[0]
	mapEntries := *m.MustMap()
	sigBytes := []byte(*mapEntries[1].Val.Bytes)
	pubBytes := []byte(*mapEntries[0].Val.Bytes)

	require.True(t, ed25519.Verify(pubBytes, digest[:], sigBytes), "embedded signature must verify")
}

func TestSignAuthEntries_NotYetSimulated(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	at.Simulation = nil
	err := at.SignAuthEntries(context.Background(), newRecordingSigner(t, keypair.MustRandom().Address()), 1)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindNotYetSimulated, e.Kind)
}

func TestSignAuthEntries_NilSignerRejected(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	err := at.SignAuthEntries(context.Background(), nil, 1)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
}

// ---------- Sign --------------------------------------------------------

func TestSign_AppendsDecoratedSignature(t *testing.T) {
	signerKP := keypair.MustRandom()
	at, _ := fixtureSimulated(t, nil, nil)

	require.Empty(t, at.Built.Signatures())
	require.NoError(t, at.Sign(KeypairSigner(signerKP)))
	require.Len(t, at.Built.Signatures(), 1)
}

func TestSign_Idempotent(t *testing.T) {
	signerKP := keypair.MustRandom()
	at, _ := fixtureSimulated(t, nil, nil)

	sig := newRecordingSigner(t, signerKP.Address())
	require.NoError(t, at.Sign(sig))
	require.Equal(t, 1, sig.txCalls)
	require.Len(t, at.Built.Signatures(), 1)

	// Second call should detect the hint already present and no-op.
	require.NoError(t, at.Sign(sig))
	assert.Equal(t, 1, sig.txCalls)
	assert.Len(t, at.Built.Signatures(), 1)
}

func TestSign_NotYetSimulated(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	at.Simulation = nil
	err := at.Sign(KeypairSigner(keypair.MustRandom()))
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindNotYetSimulated, e.Kind)
}

func TestSign_NilSignerRejected(t *testing.T) {
	at, _ := fixtureSimulated(t, nil, nil)
	err := at.Sign(nil)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
}

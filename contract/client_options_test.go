package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------
// ClientOption — T4.4 additions
// ----------------------------------------------------------------------

func TestWithSource_StrkeyRoundTrips(t *testing.T) {
	cid := testContractID(t)
	addr := keypair.MustRandom().Address()

	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithSource(addr))
	require.NotNil(t, c.source, "WithSource must populate the source account")
	assert.Equal(t, addr, c.source.GetAccountID())
}

func TestWithSource_RejectsInvalidStrkey(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, cannedInvokeRPC(t), network.TestNetworkPassphrase,
		WithSource("not-a-strkey"),
		WithSpec(buildBumpSpec(t)),
	)

	// New does not error; Invoke surfaces the deferred validation.
	_, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)})
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, ce.Error(), "WithSource")
}

func TestWithSourceAccount_StillWorks(t *testing.T) {
	// Back-compat shim for callers that own sequence management.
	cid := testContractID(t)
	src := newClientSource(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithSourceAccount(src))
	assert.Same(t, txnbuild.Account(src), c.source)
}

func TestWithSigner_RoundTrip(t *testing.T) {
	cid := testContractID(t)
	kp := keypair.MustRandom()
	s := KeypairSigner(kp)

	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithSigner(s))
	got := c.Signer()
	require.NotNil(t, got)
	assert.Equal(t, kp.Address(), got.Address())
}

func TestWithSigner_DefaultIsNil(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase)
	assert.Nil(t, c.Signer(), "no WithSigner -> Signer() returns nil")
}

func TestWithTimeout_RoundTrip(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithTimeout(7*time.Second))
	assert.Equal(t, 7*time.Second, c.Timeout())
}

func TestWithTimeout_NonPositiveIgnored(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithTimeout(0), WithTimeout(-1))
	assert.Equal(t, time.Duration(0), c.Timeout())
}

func TestWithPollOptions_RoundTrip(t *testing.T) {
	cid := testContractID(t)
	opts := rpcclient.NewPollTransactionOptions().
		WithInitialInterval(10 * time.Millisecond).
		WithMaxInterval(100 * time.Millisecond)

	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithPollOptions(opts))
	got, ok := c.PollOptions()
	require.True(t, ok, "PollOptions must report 'set' when WithPollOptions was passed")
	assert.Equal(t, 10*time.Millisecond, got.InitialInterval())
	assert.Equal(t, 100*time.Millisecond, got.MaxInterval())
}

func TestPollOptions_DefaultUnset(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase)
	_, ok := c.PollOptions()
	assert.False(t, ok, "no WithPollOptions -> ok=false")
}

// ----------------------------------------------------------------------
// InvokeAndConfirm signer fallback
// ----------------------------------------------------------------------

func TestInvokeAndConfirm_FallsBackToClientSigner(t *testing.T) {
	// With WithSigner set, a nil signer arg should not trip the "signer is
	// nil" guard. We don't need to drive the full lifecycle: any error
	// returned must not be the InvalidArgs/"signer is nil" sentinel.
	cid := testContractID(t)
	kp := keypair.MustRandom()
	c := New(cid, &fakeSimulator{err: errors.New("stop early")}, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSource(kp.Address()),
		WithSigner(KeypairSigner(kp)),
	)

	_, _, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, nil)
	require.Error(t, err)
	var ce *Error
	if errors.As(err, &ce) {
		assert.NotContains(t, ce.Error(), "signer is nil",
			"WithSigner should let InvokeAndConfirm proceed past the nil-signer guard")
	}
}

func TestInvokeAndConfirm_NoSignerNoFallback(t *testing.T) {
	// Without WithSigner and without an explicit signer arg, the nil-signer
	// guard still fires.
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSource(keypair.MustRandom().Address()),
	)
	_, _, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, nil)
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, ce.Error(), "signer is nil")
}

// ----------------------------------------------------------------------
// InvokeOption — T4.4 additions
// ----------------------------------------------------------------------

// invokeOptionClient returns a client wired with a canned RPC so per-call
// options can be observed on the returned *AssembledTransaction.
func invokeOptionClient(t *testing.T) *Client {
	t.Helper()
	return New(testContractID(t), cannedInvokeRPC(t), network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSource(keypair.MustRandom().Address()),
	)
}

func TestInvokeOption_MaxFee(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, MaxFee(123_456))
	require.NoError(t, err)
	assert.Equal(t, int64(123_456), at.maxFee)
}

func TestInvokeOption_ResourceFeeMultiplier(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, ResourceFeeMultiplier(2.0))
	require.NoError(t, err)
	assert.Equal(t, 2.0, at.resourceFeeMultiplier)
}

func TestInvokeOption_ResourceFeeMultiplier_DefaultPreserved(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)})
	require.NoError(t, err)
	assert.Equal(t, DefaultResourceFeeMultiplier, at.resourceFeeMultiplier,
		"omitting ResourceFeeMultiplier should keep AssembleParams default")
}

func TestInvokeOption_Memo(t *testing.T) {
	c := invokeOptionClient(t)
	memo := txnbuild.MemoText("hello")
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, Memo(memo))
	require.NoError(t, err)
	assert.Equal(t, memo, at.memo)
}

func TestInvokeOption_TimeBounds(t *testing.T) {
	c := invokeOptionClient(t)
	tmin := time.Unix(1_700_000_000, 0)
	tmax := time.Unix(1_700_000_500, 0)

	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, TimeBounds(tmin, tmax))
	require.NoError(t, err)

	tb := at.preconditions.TimeBounds
	assert.Equal(t, int64(1_700_000_000), tb.MinTime)
	assert.Equal(t, int64(1_700_000_500), tb.MaxTime)
}

func TestInvokeOption_SourceOverride(t *testing.T) {
	c := invokeOptionClient(t)
	overrideAddr := keypair.MustRandom().Address()

	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, Source(overrideAddr))
	require.NoError(t, err)
	assert.Equal(t, overrideAddr, at.source.GetAccountID(),
		"per-call Source must override client default")
	// Client default unchanged.
	assert.NotEqual(t, overrideAddr, c.source.GetAccountID())
}

func TestInvokeOption_Source_RejectsInvalidStrkey(t *testing.T) {
	c := invokeOptionClient(t)
	_, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, Source("bogus"))
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
}

func TestInvokeOption_WithInvokeSigner(t *testing.T) {
	c := invokeOptionClient(t)
	kp := keypair.MustRandom()
	s := KeypairSigner(kp)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, WithInvokeSigner(s))
	require.NoError(t, err)
	require.NotNil(t, at.signer)
	assert.Equal(t, kp.Address(), at.signer.Address())
}

func TestInvokeOption_AdditionalAuth(t *testing.T) {
	// Use SkipSimulate so the auth entries don't have to encode to a valid
	// envelope — we only need to verify they were pre-seeded onto the op.
	c := invokeOptionClient(t)
	entry := xdr.SorobanAuthorizationEntry{
		Credentials: xdr.SorobanCredentials{
			Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
		},
	}
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)},
		SkipSimulate(),
		AdditionalAuth(entry),
	)
	require.NoError(t, err)
	require.Len(t, at.op.Auth, 1, "AdditionalAuth must pre-seed the host-function op's Auth slice")
	assert.Equal(t, entry.Credentials.Type, at.op.Auth[0].Credentials.Type)
}

func TestInvokeOption_Restore_DefaultEnabled(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)})
	require.NoError(t, err)
	assert.True(t, at.restoreEnabled, "default Restore behavior is enabled")
}

func TestInvokeOption_Restore_OptOut(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, Restore(false))
	require.NoError(t, err)
	assert.False(t, at.restoreEnabled)
}

func TestInvokeOption_SkipSimulate(t *testing.T) {
	c := invokeOptionClient(t)
	at, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)}, SkipSimulate())
	require.NoError(t, err)
	assert.Nil(t, at.Simulation, "SkipSimulate must not invoke Simulate")
	assert.Equal(t, 0, c.RPC.(*fakeSimulator).calls)
}

package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContractID returns a deterministic strkey-encoded contract id ("C...")
// for client-construction tests.
func testContractID(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	cid, err := strkey.Encode(strkey.VersionByteContract, raw)
	require.NoError(t, err)
	return cid
}

// newClientSource returns a SimpleAccount usable as the client's source for
// transaction building.
func newClientSource(t *testing.T) *txnbuild.SimpleAccount {
	t.Helper()
	kp := keypair.MustRandom()
	a := txnbuild.NewSimpleAccount(kp.Address(), 99)
	return &a
}

// fakeFromRPC extends fakeSimulator with GetLedgerEntries so it satisfies the
// fromRPC interface used by From.
type fakeFromRPC struct {
	*fakeSimulator
	getKeys  []string
	calls    int
	resps    []protocol.GetLedgerEntriesResponse
	errs     []error
	gotReqs  []protocol.GetLedgerEntriesRequest
	fallback protocol.GetLedgerEntriesResponse
}

func (f *fakeFromRPC) GetLedgerEntries(
	_ context.Context,
	req protocol.GetLedgerEntriesRequest,
) (protocol.GetLedgerEntriesResponse, error) {
	idx := f.calls
	f.calls++
	f.gotReqs = append(f.gotReqs, req)
	if idx < len(f.resps) {
		var err error
		if idx < len(f.errs) {
			err = f.errs[idx]
		}
		return f.resps[idx], err
	}
	return f.fallback, nil
}

// ----------------------------------------------------------------------
// New
// ----------------------------------------------------------------------

func TestNew_ExplicitSpecWins(t *testing.T) {
	cid := testContractID(t)
	spec := buildBumpSpec(t)

	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithSpec(spec))

	assert.Equal(t, cid, c.ContractID)
	assert.Equal(t, network.TestNetworkPassphrase, c.Network)
	assert.Same(t, spec, c.Spec())
	assert.Equal(t, int64(txnbuild.MinBaseFee), c.baseFee, "baseFee defaults to MinBaseFee")
}

func TestNew_FallsBackToRegistry(t *testing.T) {
	cid := testContractID(t)
	spec := buildBumpSpec(t)

	RegisterSpec(cid, spec)
	t.Cleanup(func() { RegisterSpec(cid, nil) })

	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase)
	assert.Same(t, spec, c.Spec(), "constructor should consult LookupSpec when WithSpec is omitted")
}

func TestNew_NoSpecAvailable(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase)
	assert.Nil(t, c.Spec(), "no WithSpec and no registry entry leaves Spec() nil")
}

func TestNew_WithBaseFee(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase, WithBaseFee(500))
	assert.Equal(t, int64(500), c.baseFee)
}

// ----------------------------------------------------------------------
// Invoke
// ----------------------------------------------------------------------

// cannedInvokeRPC returns a fakeSimulator whose SimulateTransaction response
// is a minimally valid success.
func cannedInvokeRPC(t *testing.T) *fakeSimulator {
	t.Helper()
	_, dataB64 := cannedSorobanData(t)
	_, retB64 := cannedReturnValue(t)
	return &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			TransactionDataXDR: dataB64,
			MinResourceFee:     500_000,
			Results: []protocol.SimulateHostFunctionResult{
				{ReturnValueXDR: &retB64},
			},
		},
	}
}

func TestInvoke_BuildsHostFunctionAndSimulates(t *testing.T) {
	cid := testContractID(t)
	rpc := cannedInvokeRPC(t)

	c := New(cid, rpc, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSourceAccount(newClientSource(t)),
	)

	args := map[string]any{"amount": uint32(7)}
	at, err := c.Invoke(context.Background(), "bump", args)
	require.NoError(t, err)
	require.NotNil(t, at)

	assert.Equal(t, "bump", at.Method)
	require.Len(t, at.Args, 1)
	assert.Equal(t, xdr.ScValTypeScvU32, at.Args[0].Type)

	// The transaction must carry an InvokeHostFunction op targeting our
	// contract id.
	require.NotNil(t, at.Built)
	ops := at.Built.Operations()
	require.Len(t, ops, 1)
	op, ok := ops[0].(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	ic := op.HostFunction.InvokeContract
	require.NotNil(t, ic)
	require.NotNil(t, ic.ContractAddress.ContractId)

	expectedAddr, err := xdr.ScAddressFromStrkey(cid)
	require.NoError(t, err)
	assert.Equal(t, expectedAddr, ic.ContractAddress)
	assert.Equal(t, xdr.ScSymbol("bump"), ic.FunctionName)

	// Simulate ran exactly once.
	assert.Equal(t, 1, rpc.calls)
}

func TestInvoke_AcceptsRawScVals(t *testing.T) {
	cid := testContractID(t)
	rpc := cannedInvokeRPC(t)

	c := New(cid, rpc, network.TestNetworkPassphrase, // no spec on purpose
		WithSourceAccount(newClientSource(t)),
	)

	rawArgs := []xdr.ScVal{{Type: xdr.ScValTypeScvU32, U32: u32ptr(11)}}
	at, err := c.Invoke(context.Background(), "anything", rawArgs)
	require.NoError(t, err, "raw []xdr.ScVal must work without a spec")
	require.Len(t, at.Args, 1)
	assert.Equal(t, uint32(11), uint32(*at.Args[0].U32))
}

func TestInvoke_RejectsUnknownMethodWithSpec(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSourceAccount(newClientSource(t)),
	)

	_, err := c.Invoke(context.Background(), "nope", map[string]any{})
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, ce.Error(), `"nope"`)
}

func TestInvoke_RejectsMapArgsWithoutSpec(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSourceAccount(newClientSource(t)),
	)

	_, err := c.Invoke(context.Background(), "bump", map[string]any{"amount": uint32(1)})
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
}

func TestInvoke_RequiresSource(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
	)

	_, err := c.Invoke(context.Background(), "bump", []xdr.ScVal{})
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, ce.Error(), "source account")
}

func TestInvoke_EmptyMethodRejected(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSourceAccount(newClientSource(t)),
	)
	_, err := c.Invoke(context.Background(), "", nil)
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
}

// ----------------------------------------------------------------------
// From
// ----------------------------------------------------------------------

func TestFrom_RegistryShortCircuitsNetwork(t *testing.T) {
	cid := testContractID(t)
	spec := buildBumpSpec(t)

	RegisterSpec(cid, spec)
	t.Cleanup(func() { RegisterSpec(cid, nil) })

	rpc := &fakeFromRPC{fakeSimulator: &fakeSimulator{}}
	c, err := From(context.Background(), cid, rpc, network.TestNetworkPassphrase)
	require.NoError(t, err)
	assert.Same(t, spec, c.Spec())
	assert.Equal(t, 0, rpc.calls, "registered spec must short-circuit any network call")
}

func TestFrom_FetchesSpecFromLedger(t *testing.T) {
	cid := testContractID(t)
	addr, err := xdr.ScAddressFromStrkey(cid)
	require.NoError(t, err)

	// Build a deterministic wasm hash for the contract instance to point at.
	var wasmHash xdr.Hash
	for i := range wasmHash {
		wasmHash[i] = byte(0xA0 + i)
	}

	// Contract instance entry: ContractData with Val=ScvContractInstance
	// whose executable hashes to wasmHash.
	instanceVal := xdr.ScVal{
		Type: xdr.ScValTypeScvContractInstance,
		Instance: &xdr.ScContractInstance{
			Executable: xdr.ContractExecutable{
				Type:     xdr.ContractExecutableTypeContractExecutableWasm,
				WasmHash: &wasmHash,
			},
		},
	}
	instanceEntry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   addr,
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        instanceVal,
		},
	}
	instanceB64, err := xdr.MarshalBase64(instanceEntry)
	require.NoError(t, err)

	// Contract code entry whose code bytes embed a valid spec custom section
	// declaring the `bump` function (same shape buildBumpSpec produces).
	specXDR := encodeEntries(t, buildBumpSpec(t).Entries())
	wasm := buildWasmWithCustomSection(t, "contractspecv0", specXDR)
	codeEntry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.ContractCodeEntry{
			Hash: wasmHash,
			Code: wasm,
		},
	}
	codeB64, err := xdr.MarshalBase64(codeEntry)
	require.NoError(t, err)

	rpc := &fakeFromRPC{
		fakeSimulator: &fakeSimulator{},
		resps: []protocol.GetLedgerEntriesResponse{
			{Entries: []protocol.LedgerEntryResult{{DataXDR: instanceB64}}},
			{Entries: []protocol.LedgerEntryResult{{DataXDR: codeB64}}},
		},
	}

	// Clear any registry entry from prior tests so we exercise the fetch
	// path.
	RegisterSpec(cid, nil)
	t.Cleanup(func() { RegisterSpec(cid, nil) })

	c, err := From(context.Background(), cid, rpc, network.TestNetworkPassphrase)
	require.NoError(t, err)
	require.NotNil(t, c.Spec(), "From must populate spec from network")
	assert.True(t, c.Spec().HasFunc("bump"), "fetched spec must contain the bump function")

	assert.Equal(t, 2, rpc.calls, "From must hit GetLedgerEntries twice (instance + code)")
	assert.Same(t, c.Spec(), LookupSpec(cid), "From must side-effect register the spec")
}

func TestFrom_RequiresRPC(t *testing.T) {
	cid := testContractID(t)
	_, err := From(context.Background(), cid, nil, network.TestNetworkPassphrase)
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
}

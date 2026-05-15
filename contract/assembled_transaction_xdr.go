package contract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// assembledTxBlob is the canonical serializable shape for hand-off between
// hosts. It mirrors the JS SDK's AssembledTransaction.toJSON output (see
// js-stellar-sdk/src/contract/assembled_transaction.ts):
//
//	{
//	  "method": "...",
//	  "tx": "<TransactionEnvelope b64>",
//	  "simulationResult": {
//	    "auth":   ["<SorobanAuthorizationEntry b64>", ...],
//	    "retval": "<ScVal b64>"
//	  },
//	  "simulationTransactionData": "<SorobanTransactionData b64>"
//	}
//
// The XDR form is the JSON blob base64-encoded so it can be passed as a
// single string. The blob's fields (tx, auth, retval, simulation data)
// are themselves base64-encoded XDR, so the wrapper carries no further
// XDR-typed payload.
type assembledTxBlob struct {
	Method                    string              `json:"method"`
	Tx                        string              `json:"tx"`
	SimulationResult          simulationResultB64 `json:"simulationResult"`
	SimulationTransactionData string              `json:"simulationTransactionData"`
}

// simulationResultB64 carries the per-host-function simulation output that
// is needed to recover an AssembledTransaction's Simulation snapshot.
type simulationResultB64 struct {
	Auth   []string `json:"auth"`
	Retval string   `json:"retval"`
}

// FromOption configures FromXDR / FromJSON. Options that cannot be derived
// from the serialized payload — network passphrase, Spec, resource-fee
// multiplier — are supplied here.
type FromOption func(*fromConfig)

// FromXDROption is an alias kept for design-doc parity (§4.3 names the type
// FromXDROption for ToXDR/FromXDR specifically).
type FromXDROption = FromOption

// FromJSONOption is an alias kept for design-doc parity (§4.3 names the type
// FromJSONOption for ToJSON/FromJSON specifically).
type FromJSONOption = FromOption

type fromConfig struct {
	network               string
	spec                  *Spec
	resourceFeeMultiplier float64
}

// WithNetworkPassphrase sets the network passphrase used for signing on the
// rehydrated AssembledTransaction. It is REQUIRED — the serialized blob
// does not (and should not) carry the network. Passing an empty string is
// equivalent to omitting the option and will fail with KindInvalidArgs.
func WithNetworkPassphrase(network string) FromOption {
	return func(c *fromConfig) { c.network = network }
}

// WithSpecOverride attaches an optional Spec to the rehydrated transaction so
// Result() can decode the return value into a native Go type. When omitted,
// FromXDR / FromJSON consult the package-level spec registry using the
// envelope's contract ID; if neither yields a Spec the transaction works
// fine but Result() returns raw xdr.ScVal. The matching client-level option
// is named WithSpec — this variant is the FromXDR/FromJSON-specific override.
func WithSpecOverride(s *Spec) FromOption {
	return func(c *fromConfig) { c.spec = s }
}

// WithResourceFeeMultiplier overrides the resource-fee pad applied by a
// future Simulate() call. The serialized envelope already has the fee
// baked in, so this only matters if the caller plans to re-simulate.
// Values <= 0 fall back to DefaultResourceFeeMultiplier.
func WithResourceFeeMultiplier(mult float64) FromOption {
	return func(c *fromConfig) { c.resourceFeeMultiplier = mult }
}

// ToXDR serializes the AssembledTransaction to a base64 XDR string for
// hand-off. Requires Simulate to have run; returns ErrNotYetSimulated
// otherwise. Matches the JS SDK shape (envelope + simulation result +
// simulation transaction data) wrapped in a single XDR-marshaled struct.
func (a *AssembledTransaction) ToXDR() (string, error) {
	blob, err := a.toBlob()
	if err != nil {
		return "", err
	}
	// The on-wire "XDR" form is the JSON blob base64-encoded. The blob
	// fields are themselves base64 XDR (envelope, soroban data, auth
	// entries, retval) — there is no value in inventing a wrapper XDR
	// type whose sole job is to hold four base64 strings.
	raw, err := json.Marshal(blob)
	if err != nil {
		return "", fmt.Errorf("contract: encode AT blob: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// FromXDR rehydrates an AssembledTransaction from the base64 XDR string
// produced by ToXDR. rpc is required so the rehydrated transaction can be
// signed and sent; WithNetworkPassphrase is required so signatures attach
// to the correct network.
func FromXDR(ctx context.Context, rpc rpcSimulator, b64 string, opts ...FromXDROption) (*AssembledTransaction, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR: decode payload", cause: err}
	}
	var blob assembledTxBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR: parse AT blob", cause: err}
	}
	return fromBlob(ctx, rpc, blob, opts...)
}

// ToJSON serializes the AssembledTransaction as a JSON byte slice for
// hand-off across a textual transport. The shape matches the JS SDK
// AssembledTransaction.toJSON() output exactly (method, tx, simulationResult
// { auth, retval }, simulationTransactionData).
func (a *AssembledTransaction) ToJSON() ([]byte, error) {
	blob, err := a.toBlob()
	if err != nil {
		return nil, err
	}
	return json.Marshal(blob)
}

// FromJSON rehydrates an AssembledTransaction from the JSON bytes produced
// by ToJSON. rpc is required so the rehydrated transaction can be signed
// and sent; WithNetworkPassphrase is required so signatures attach to the
// correct network.
func FromJSON(ctx context.Context, rpc rpcSimulator, payload []byte, opts ...FromJSONOption) (*AssembledTransaction, error) {
	var blob assembledTxBlob
	if err := json.Unmarshal(payload, &blob); err != nil {
		return nil, &Error{Kind: KindInvalidArgs, Details: "FromJSON: parse AT blob", cause: err}
	}
	return fromBlob(ctx, rpc, blob, opts...)
}

// toBlob extracts the serializable view of the AssembledTransaction.
// Requires Simulate to have run successfully (Built + Simulation non-nil),
// otherwise returns ErrNotYetSimulated.
func (a *AssembledTransaction) toBlob() (assembledTxBlob, error) {
	if a == nil {
		return assembledTxBlob{}, invalidArgsf("AssembledTransaction is nil")
	}
	if a.Built == nil || a.Simulation == nil {
		return assembledTxBlob{}, &Error{Kind: KindNotYetSimulated, Details: "ToXDR/ToJSON"}
	}

	envB64, err := a.Built.Base64()
	if err != nil {
		return assembledTxBlob{}, fmt.Errorf("contract: encode envelope: %w", err)
	}

	authB64 := make([]string, 0, len(a.AuthEntries))
	for i, entry := range a.AuthEntries {
		b, err := xdr.MarshalBase64(entry)
		if err != nil {
			return assembledTxBlob{}, fmt.Errorf("contract: encode auth entry %d: %w", i, err)
		}
		authB64 = append(authB64, b)
	}

	var retvalB64 string
	if a.ReturnValue != nil {
		retvalB64, err = xdr.MarshalBase64(*a.ReturnValue)
		if err != nil {
			return assembledTxBlob{}, fmt.Errorf("contract: encode return value: %w", err)
		}
	}

	// Pull the SorobanTransactionData straight off the in-flight op so it
	// matches what Simulate folded in (including the fee multiplier).
	var sorobanDataB64 string
	if a.op != nil && a.op.Ext.V == 1 && a.op.Ext.SorobanData != nil {
		sorobanDataB64, err = xdr.MarshalBase64(*a.op.Ext.SorobanData)
		if err != nil {
			return assembledTxBlob{}, fmt.Errorf("contract: encode soroban data: %w", err)
		}
	}

	return assembledTxBlob{
		Method:                    a.Method,
		Tx:                        envB64,
		SimulationResult:          simulationResultB64{Auth: authB64, Retval: retvalB64},
		SimulationTransactionData: sorobanDataB64,
	}, nil
}

// fromBlob is the shared decode path for FromXDR / FromJSON. It rebuilds
// the AssembledTransaction state that Simulate normally produces.
func fromBlob(_ context.Context, rpc rpcSimulator, blob assembledTxBlob, opts ...FromXDROption) (*AssembledTransaction, error) {
	if rpc == nil {
		return nil, invalidArgsf("FromXDR/FromJSON: rpc is required")
	}

	cfg := fromConfig{resourceFeeMultiplier: DefaultResourceFeeMultiplier}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.network == "" {
		return nil, invalidArgsf("FromXDR/FromJSON: WithNetworkPassphrase is required")
	}
	if cfg.resourceFeeMultiplier <= 0 {
		cfg.resourceFeeMultiplier = DefaultResourceFeeMultiplier
	}

	if blob.Tx == "" {
		return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR/FromJSON: tx envelope is empty"}
	}

	parsed, err := txnbuild.TransactionFromXDR(blob.Tx)
	if err != nil {
		return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR/FromJSON: parse tx envelope", cause: err}
	}
	tx, ok := parsed.Transaction()
	if !ok {
		return nil, invalidArgsf("FromXDR/FromJSON: fee-bump envelopes are not supported")
	}

	ops := tx.Operations()
	if len(ops) != 1 {
		return nil, invalidArgsf("FromXDR/FromJSON: expected exactly one operation, got %d", len(ops))
	}
	op, ok := ops[0].(*txnbuild.InvokeHostFunction)
	if !ok {
		return nil, invalidArgsf("FromXDR/FromJSON: operation is not InvokeHostFunction")
	}

	// Decode auth entries.
	authEntries := make([]xdr.SorobanAuthorizationEntry, 0, len(blob.SimulationResult.Auth))
	for i, encoded := range blob.SimulationResult.Auth {
		var entry xdr.SorobanAuthorizationEntry
		if err := xdr.SafeUnmarshalBase64(encoded, &entry); err != nil {
			return nil, &Error{Kind: KindInvalidArgs, Details: fmt.Sprintf("FromXDR/FromJSON: decode auth %d", i), cause: err}
		}
		authEntries = append(authEntries, entry)
	}

	// Decode return value (optional).
	var returnValue *xdr.ScVal
	if blob.SimulationResult.Retval != "" {
		var v xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(blob.SimulationResult.Retval, &v); err != nil {
			return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR/FromJSON: decode return value", cause: err}
		}
		returnValue = &v
	}

	// Decode SorobanTransactionData (optional).
	var sorobanData *xdr.SorobanTransactionData
	if blob.SimulationTransactionData != "" {
		var d xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(blob.SimulationTransactionData, &d); err != nil {
			return nil, &Error{Kind: KindInvalidArgs, Details: "FromXDR/FromJSON: decode soroban data", cause: err}
		}
		sorobanData = &d
	}

	method, args := extractInvocation(op.HostFunction)
	// Method recorded in the blob must match the envelope to catch tampering
	// where someone swaps the method name without re-signing.
	if blob.Method != "" && blob.Method != method {
		return nil, invalidArgsf("FromXDR/FromJSON: method mismatch: blob=%q envelope=%q", blob.Method, method)
	}

	// Synthesize a SimulateTransactionResponse so the rehydrated tx has the
	// same Simulation snapshot a fresh Simulate() would have produced.
	sim := &protocol.SimulateTransactionResponse{}
	if sorobanData != nil {
		b, err := xdr.MarshalBase64(*sorobanData)
		if err != nil {
			return nil, fmt.Errorf("contract: re-encode soroban data: %w", err)
		}
		sim.TransactionDataXDR = b
	}
	if len(blob.SimulationResult.Auth) > 0 || blob.SimulationResult.Retval != "" {
		result := protocol.SimulateHostFunctionResult{}
		if len(blob.SimulationResult.Auth) > 0 {
			auth := make([]string, len(blob.SimulationResult.Auth))
			copy(auth, blob.SimulationResult.Auth)
			result.AuthXDR = &auth
		}
		if blob.SimulationResult.Retval != "" {
			r := blob.SimulationResult.Retval
			result.ReturnValueXDR = &r
		}
		sim.Results = []protocol.SimulateHostFunctionResult{result}
	}

	// Resolve spec: explicit override > package registry lookup by contract
	// ID (when present on the host function) > nil (Result() returns raw).
	spec := cfg.spec
	if spec == nil {
		if cid := contractIDFromOp(op); cid != "" {
			spec = LookupSpec(cid)
		}
	}

	src := tx.SourceAccount()
	srcAcct := &src

	return &AssembledTransaction{
		Method:                method,
		Args:                  args,
		Built:                 tx,
		Simulation:            sim,
		AuthEntries:           authEntries,
		ReturnValue:           returnValue,
		rpc:                   rpc,
		op:                    op,
		source:                srcAcct,
		network:               cfg.network,
		baseFee:               tx.BaseFee(),
		memo:                  tx.Memo(),
		preconditions:         txnbuild.Preconditions{TimeBounds: tx.Timebounds()},
		resourceFeeMultiplier: cfg.resourceFeeMultiplier,
		spec:                  spec,
	}, nil
}

// contractIDFromOp returns the strkey "C..." contract ID of the invocation
// target when op is an InvokeContract host function. Returns "" otherwise.
func contractIDFromOp(op *txnbuild.InvokeHostFunction) string {
	fn := op.HostFunction
	if fn.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract || fn.InvokeContract == nil {
		return ""
	}
	addr := fn.InvokeContract.ContractAddress
	s, err := addr.String()
	if err != nil {
		return ""
	}
	return s
}

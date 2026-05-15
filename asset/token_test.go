package asset_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/asset"
	"github.com/stellar/go-stellar-sdk/clients/horizonclient"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func testRPC(t *testing.T) *rpcclient.Client {
	t.Helper()
	return rpcclient.NewClient("http://localhost:8000/soroban/rpc", nil)
}

func TestNew_NativeAsset(t *testing.T) {
	rpc := testRPC(t)
	tok, err := asset.New(xdr.MustNewNativeAsset(),
		asset.WithRPC(rpc),
		asset.WithNetwork(network.PublicNetworkPassphrase),
	)
	if err != nil {
		t.Fatalf("asset.New(native): %v", err)
	}
	if !tok.IsClassic() {
		t.Fatal("expected IsClassic true for native asset")
	}
	if tok.Asset.Type != xdr.AssetTypeAssetTypeNative {
		t.Fatalf("Asset.Type = %v, want native", tok.Asset.Type)
	}
	if tok.Network() != network.PublicNetworkPassphrase {
		t.Fatalf("Network() = %q, want public passphrase", tok.Network())
	}
	if tok.Client() == nil {
		t.Fatal("Client() is nil")
	}
	if tok.Client().ContractID != tok.ContractID {
		t.Fatalf("Client.ContractID = %q, want %q", tok.Client().ContractID, tok.ContractID)
	}
	// SAC contract id must be a valid C-strkey.
	if !strings.HasPrefix(tok.ContractID, "C") {
		t.Fatalf("ContractID = %q, want C… prefix", tok.ContractID)
	}
	if _, err := strkey.Decode(strkey.VersionByteContract, tok.ContractID); err != nil {
		t.Fatalf("strkey.Decode(contract, %q): %v", tok.ContractID, err)
	}
	// Same asset + passphrase must yield a stable id across calls.
	again, err := asset.New(xdr.MustNewNativeAsset(),
		asset.WithRPC(rpc),
		asset.WithNetwork(network.PublicNetworkPassphrase),
	)
	if err != nil {
		t.Fatalf("asset.New(native) again: %v", err)
	}
	if again.ContractID != tok.ContractID {
		t.Fatalf("non-deterministic ContractID: %q vs %q", again.ContractID, tok.ContractID)
	}
	// Different network -> different id.
	testTok, err := asset.New(xdr.MustNewNativeAsset(),
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
	)
	if err != nil {
		t.Fatalf("asset.New(native, testnet): %v", err)
	}
	if testTok.ContractID == tok.ContractID {
		t.Fatalf("expected pubnet and testnet SAC ids to differ; got %q for both", tok.ContractID)
	}
}

func TestNew_CreditAsset(t *testing.T) {
	rpc := testRPC(t)
	// Use a valid G-strkey issuer; the specific account doesn't matter — the
	// contract id derivation is a pure function of asset code + issuer +
	// network.
	issuer := "GA5XIGA5C7QTPTWXQHY6MCJRMTRZDOSHR6EFIBNDQTCQHG262N4GGKTM"
	usdc, err := xdr.BuildAsset("credit_alphanum4", issuer, "USDC")
	if err != nil {
		t.Fatalf("xdr.BuildAsset: %v", err)
	}
	tok, err := asset.New(usdc,
		asset.WithRPC(rpc),
		asset.WithNetwork(network.PublicNetworkPassphrase),
		asset.WithSource("GA5XIGA5C7QTPTWXQHY6MCJRMTRZDOSHR6EFIBNDQTCQHG262N4GGKTM"),
	)
	if err != nil {
		t.Fatalf("asset.New(USDC): %v", err)
	}
	if !tok.IsClassic() {
		t.Fatal("expected IsClassic true for credit asset")
	}
	if !strings.HasPrefix(tok.ContractID, "C") {
		t.Fatalf("ContractID = %q, want C… prefix", tok.ContractID)
	}
	if _, err := strkey.Decode(strkey.VersionByteContract, tok.ContractID); err != nil {
		t.Fatalf("strkey.Decode: %v", err)
	}
	// Distinct from native SAC id on the same network.
	nativeTok, err := asset.New(xdr.MustNewNativeAsset(),
		asset.WithRPC(rpc),
		asset.WithNetwork(network.PublicNetworkPassphrase),
	)
	if err != nil {
		t.Fatalf("asset.New(native): %v", err)
	}
	if nativeTok.ContractID == tok.ContractID {
		t.Fatalf("USDC and XLM SAC ids collided: %q", tok.ContractID)
	}
	if tok.Source() != "GA5XIGA5C7QTPTWXQHY6MCJRMTRZDOSHR6EFIBNDQTCQHG262N4GGKTM" {
		t.Fatalf("Source() = %q, want issuer", tok.Source())
	}
}

func TestNewFromContractID(t *testing.T) {
	rpc := testRPC(t)
	// A valid C-strkey from existing repo test vectors.
	const cid = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"
	tok, err := asset.NewFromContractID(cid,
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
		asset.WithHorizon(horizonclient.DefaultTestNetClient),
	)
	if err != nil {
		t.Fatalf("asset.NewFromContractID: %v", err)
	}
	if tok.IsClassic() {
		t.Fatal("expected IsClassic false for SAC-only token")
	}
	if tok.ContractID != cid {
		t.Fatalf("ContractID = %q, want %q", tok.ContractID, cid)
	}
	if tok.Client() == nil || tok.Client().ContractID != cid {
		t.Fatalf("Client.ContractID = %v, want %q", tok.Client(), cid)
	}
	if tok.Horizon() != horizonclient.DefaultTestNetClient {
		t.Fatal("Horizon() did not return the bound client")
	}
}

func TestNewFromContractID_CustomSpec(t *testing.T) {
	rpc := testRPC(t)
	const cid = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"
	// An empty Spec is a distinct pointer from SACSpec(); we just check
	// WithSpec wins.
	custom := &contract.Spec{}
	tok, err := asset.NewFromContractID(cid,
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
		asset.WithSpec(custom),
	)
	if err != nil {
		t.Fatalf("asset.NewFromContractID: %v", err)
	}
	if tok.Client().Spec() != custom {
		t.Fatal("WithSpec did not propagate to underlying contract.Client")
	}
}

func TestNew_RejectsMissingOptions(t *testing.T) {
	rpc := testRPC(t)

	cases := []struct {
		name string
		opts []asset.Option
		want string
	}{
		{
			name: "no rpc",
			opts: []asset.Option{asset.WithNetwork(network.PublicNetworkPassphrase)},
			want: "WithRPC is required",
		},
		{
			name: "no network",
			opts: []asset.Option{asset.WithRPC(rpc)},
			want: "WithNetwork is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := asset.New(xdr.MustNewNativeAsset(), tc.opts...)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, asset.ErrInvalidToken) {
				t.Fatalf("err not ErrInvalidToken: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNewFromContractID_RejectsBadStrkey(t *testing.T) {
	rpc := testRPC(t)
	cases := []string{
		"",
		"not-a-strkey",
		// A valid G-strkey is the wrong version byte for a contract id.
		"GA5XIGA5C7QTPTWXQHY6MCJRMTRZDOSHR6EFIBNDQTCQHG262N4GGKTM",
	}
	for _, cid := range cases {
		t.Run(cid, func(t *testing.T) {
			_, err := asset.NewFromContractID(cid,
				asset.WithRPC(rpc),
				asset.WithNetwork(network.TestNetworkPassphrase),
			)
			if err == nil {
				t.Fatalf("expected error for %q", cid)
			}
			if !errors.Is(err, asset.ErrInvalidToken) {
				t.Fatalf("err not ErrInvalidToken: %v", err)
			}
		})
	}
}

package asset

import (
	_ "embed"
	"fmt"
	"sync"

	"github.com/stellar/go-stellar-sdk/contract"
)

// sacSpecBytes holds the XDR-encoded stream of ScSpecEntry records that
// describes the Stellar Asset Contract. The bytes are the base64-decoded
// payload of `SAC_SPEC` from the JS SDK's `src/bindings/sac-spec.ts`, which is
// itself generated from upstream stellar-core. Vendored so that classic-asset
// transfers work without a network round-trip or codegen step.
//
//go:embed sac_spec.bin
var sacSpecBytes []byte

var (
	sacSpecOnce sync.Once
	sacSpec     *contract.Spec
	sacSpecErr  error
)

// SACSpec returns the bundled Stellar Asset Contract spec. The returned *Spec
// is parsed lazily on first call, cached, and safe to share across goroutines.
// It panics only if the embedded bytes fail to decode, which would indicate a
// corrupt build artifact.
func SACSpec() *contract.Spec {
	sacSpecOnce.Do(func() {
		sacSpec, sacSpecErr = contract.NewSpecFromBytes(sacSpecBytes)
	})
	if sacSpecErr != nil {
		panic(fmt.Errorf("asset: decoding embedded SAC spec: %w", sacSpecErr))
	}
	return sacSpec
}

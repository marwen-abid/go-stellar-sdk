package contract

import "sync"

// The spec registry is a process-global map from contract ID (strkey "C...")
// to *Spec. Codegen-emitted packages (see design doc T8.3) call RegisterSpec
// from init(), so importing a generated package — e.g. github.com/.../usdc —
// automatically populates the registry and lets the higher-level Client skip
// the network round-trip otherwise required to fetch the contract's spec from
// its wasm. Non-codegen consumers can also register specs manually.
//
// Mirrors the database/sql driver-registration pattern.

var (
	specRegistryMu sync.RWMutex
	specRegistry   = map[string]*Spec{}
)

// RegisterSpec attaches s to contractID for the lifetime of the process.
// Subsequent registrations for the same contract ID overwrite the previous
// value. RegisterSpec is safe to call concurrently.
func RegisterSpec(contractID string, s *Spec) {
	specRegistryMu.Lock()
	specRegistry[contractID] = s
	specRegistryMu.Unlock()
}

// LookupSpec returns the Spec previously registered for contractID, or nil if
// no Spec has been registered. LookupSpec is safe to call concurrently.
func LookupSpec(contractID string) *Spec {
	specRegistryMu.RLock()
	s := specRegistry[contractID]
	specRegistryMu.RUnlock()
	return s
}

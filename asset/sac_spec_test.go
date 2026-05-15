package asset

import (
	"testing"
)

// expectedSACFuncs is the canonical set of function names exported by the
// Stellar Asset Contract, matching the JS SDK's bundled sac-spec.ts. Signature
// validation is deferred to higher-level Token tests.
var expectedSACFuncs = []string{
	"allowance",
	"approve",
	"authorized",
	"balance",
	"burn",
	"burn_from",
	"clawback",
	"decimals",
	"mint",
	"name",
	"set_admin",
	"admin",
	"set_authorized",
	"symbol",
	"transfer",
	"transfer_from",
}

func TestSACSpec_NonNil(t *testing.T) {
	s := SACSpec()
	if s == nil {
		t.Fatal("SACSpec() returned nil")
	}
	if len(s.Entries()) == 0 {
		t.Fatal("SACSpec() has no entries")
	}
}

func TestSACSpec_ExposesExpectedFuncs(t *testing.T) {
	s := SACSpec()
	for _, name := range expectedSACFuncs {
		if !s.HasFunc(name) {
			t.Errorf("SACSpec missing function %q", name)
		}
	}
}

func TestSACSpec_Idempotent(t *testing.T) {
	a := SACSpec()
	b := SACSpec()
	if a != b {
		t.Fatal("SACSpec() returned different instances on repeated calls")
	}
}

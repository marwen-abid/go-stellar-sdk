package contract

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
)

// resetSpecRegistry wipes the package-global registry so tests don't leak
// state between each other. Tests that mutate the registry must call this
// in a t.Cleanup.
func resetSpecRegistry() {
	specRegistryMu.Lock()
	specRegistry = map[string]*Spec{}
	specRegistryMu.Unlock()
}

func TestSpecRegistry_RegisterAndLookup(t *testing.T) {
	t.Cleanup(resetSpecRegistry)
	resetSpecRegistry()

	spec := NewSpecFromEntries([]xdr.ScSpecEntry{fnEntry(t, "transfer")})
	const cid = "CONTRACT_ID_AAA"

	RegisterSpec(cid, spec)

	got := LookupSpec(cid)
	assert.Same(t, spec, got, "LookupSpec should return the exact pointer that was registered")
}

func TestSpecRegistry_LookupMissReturnsNil(t *testing.T) {
	t.Cleanup(resetSpecRegistry)
	resetSpecRegistry()

	assert.Nil(t, LookupSpec("CONTRACT_ID_NEVER_REGISTERED"))
}

func TestSpecRegistry_ReRegisterOverwrites(t *testing.T) {
	t.Cleanup(resetSpecRegistry)
	resetSpecRegistry()

	first := NewSpecFromEntries([]xdr.ScSpecEntry{fnEntry(t, "transfer")})
	second := NewSpecFromEntries([]xdr.ScSpecEntry{fnEntry(t, "balance")})
	const cid = "CONTRACT_ID_BBB"

	RegisterSpec(cid, first)
	RegisterSpec(cid, second)

	assert.Same(t, second, LookupSpec(cid), "second RegisterSpec should overwrite the first")
}

// TestSpecRegistry_Concurrent fans out goroutines that register and look up
// distinct contract IDs in parallel. The test exists primarily to give the
// race detector something to chew on when someone runs `go test -race`; the
// assertions themselves only need to confirm we observe what we wrote.
func TestSpecRegistry_Concurrent(t *testing.T) {
	t.Cleanup(resetSpecRegistry)
	resetSpecRegistry()

	const n = 64
	specs := make([]*Spec, n)
	for i := range specs {
		specs[i] = NewSpecFromEntries([]xdr.ScSpecEntry{fnEntry(t, fmt.Sprintf("fn%d", i))})
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			cid := fmt.Sprintf("CID_%d", i)
			RegisterSpec(cid, specs[i])
			// Read back; LookupSpec may also race with other goroutines'
			// writes, but each goroutine writes a unique key so its own
			// lookup must see its own write.
			assert.Same(t, specs[i], LookupSpec(cid))
		}(i)
	}
	wg.Wait()

	// Final sweep: every key we wrote must be present.
	for i := 0; i < n; i++ {
		assert.Same(t, specs[i], LookupSpec(fmt.Sprintf("CID_%d", i)))
	}
}

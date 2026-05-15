package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fnEntry builds a minimal function-spec ScSpecEntry for tests. Inputs and
// outputs are intentionally empty — argument marshaling is T2.2/T2.3.
func fnEntry(t *testing.T, name string) xdr.ScSpecEntry {
	t.Helper()
	fn := xdr.ScSpecFunctionV0{
		Name:    xdr.ScSymbol(name),
		Inputs:  []xdr.ScSpecFunctionInputV0{},
		Outputs: []xdr.ScSpecTypeDef{},
	}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryFunctionV0, fn)
	require.NoError(t, err)
	return e
}

// errorEnumEntry builds a contract-error UDT entry with the given case names.
func errorEnumEntry(t *testing.T, name string, cases ...string) xdr.ScSpecEntry {
	t.Helper()
	udt := xdr.ScSpecUdtErrorEnumV0{Name: name}
	for i, c := range cases {
		udt.Cases = append(udt.Cases, xdr.ScSpecUdtErrorEnumCaseV0{
			Name:  c,
			Value: xdr.Uint32(i + 1),
		})
	}
	e, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0, udt)
	require.NoError(t, err)
	return e
}

func TestSpec_NewFromEntries_Empty(t *testing.T) {
	s := NewSpecFromEntries(nil)
	require.NotNil(t, s)
	assert.Empty(t, s.Entries())
	assert.Empty(t, s.Funcs())
	assert.False(t, s.HasFunc("anything"))
	assert.Empty(t, s.ErrorCases())
}

func TestSpec_NewFromEntries_FunctionsAndLookups(t *testing.T) {
	entries := []xdr.ScSpecEntry{
		fnEntry(t, "transfer"),
		fnEntry(t, "balance"),
		errorEnumEntry(t, "Error", "NotAuthorized", "InsufficientBalance"),
	}
	s := NewSpecFromEntries(entries)

	assert.Len(t, s.Entries(), 3)

	funcs := s.Funcs()
	require.Len(t, funcs, 2)
	assert.Equal(t, "transfer", string(funcs[0].Name))
	assert.Equal(t, "balance", string(funcs[1].Name))

	assert.True(t, s.HasFunc("transfer"))
	assert.True(t, s.HasFunc("balance"))
	assert.False(t, s.HasFunc("missing"))
	// UDT entries must not satisfy HasFunc, even though they live in the
	// name index.
	assert.False(t, s.HasFunc("Error"))

	cases := s.ErrorCases()
	require.Len(t, cases, 2)
	assert.Equal(t, "NotAuthorized", cases[0].Name)
	assert.Equal(t, "InsufficientBalance", cases[1].Name)
	assert.Equal(t, uint32(1), uint32(cases[0].Value))
	assert.Equal(t, uint32(2), uint32(cases[1].Value))
}

func TestSpec_NilReceiverSafe(t *testing.T) {
	var s *Spec
	assert.Nil(t, s.Entries())
	assert.Empty(t, s.Funcs())
	assert.False(t, s.HasFunc("x"))
	assert.Empty(t, s.ErrorCases())
}

func TestSpec_NewFromBase64_RoundTrip(t *testing.T) {
	entries := []xdr.ScSpecEntry{
		fnEntry(t, "init"),
		fnEntry(t, "set"),
	}
	buf := encodeEntries(t, entries)
	b64 := base64.StdEncoding.EncodeToString(buf)

	s, err := NewSpecFromBase64(b64)
	require.NoError(t, err)
	require.Len(t, s.Entries(), 2)
	assert.True(t, s.HasFunc("init"))
	assert.True(t, s.HasFunc("set"))
}

func TestSpec_NewFromBase64_InvalidBase64(t *testing.T) {
	_, err := NewSpecFromBase64("not!base64!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base64")
}

func TestSpec_NewFromBase64_TruncatedStream(t *testing.T) {
	entries := []xdr.ScSpecEntry{fnEntry(t, "init")}
	buf := encodeEntries(t, entries)
	// Drop a trailing byte to force a mid-record EOF.
	b64 := base64.StdEncoding.EncodeToString(buf[:len(buf)-1])
	_, err := NewSpecFromBase64(b64)
	require.Error(t, err)
}

func TestSpec_NewFromWasm_RoundTrip(t *testing.T) {
	entries := []xdr.ScSpecEntry{
		fnEntry(t, "hello"),
	}
	specXDR := encodeEntries(t, entries)
	wasm := buildWasmWithCustomSection(t, "contractspecv0", specXDR)

	s, err := NewSpecFromWasm(wasm)
	require.NoError(t, err)
	assert.True(t, s.HasFunc("hello"))
}

func TestSpec_NewFromWasm_MissingSection(t *testing.T) {
	wasm := buildWasmWithCustomSection(t, "other", []byte{0x01, 0x02})
	_, err := NewSpecFromWasm(wasm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contractspecv0")
}

func TestSpec_NewFromWasm_BadMagic(t *testing.T) {
	_, err := NewSpecFromWasm([]byte("not wasm at all"))
	require.Error(t, err)
}

// --- test helpers ---

func encodeEntries(t *testing.T, entries []xdr.ScSpecEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	for i := range entries {
		_, err := xdr.Marshal(&buf, &entries[i])
		require.NoError(t, err)
	}
	return buf.Bytes()
}

// buildWasmWithCustomSection produces a minimal but well-formed WebAssembly
// module containing a single custom section with the given name and payload.
// It is just enough to exercise the wasmCustomSection walker — it does not
// declare any functions, types, exports, etc.
func buildWasmWithCustomSection(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var w bytes.Buffer
	// Magic + version.
	w.Write([]byte{0x00, 'a', 's', 'm'})
	w.Write([]byte{0x01, 0x00, 0x00, 0x00})

	// Custom section body: nameLen (LEB128) | name | payload.
	var body bytes.Buffer
	writeVarUint32(&body, uint32(len(name)))
	body.WriteString(name)
	body.Write(payload)

	// Section header: id=0 | size (LEB128) | body.
	w.WriteByte(0x00)
	writeVarUint32(&w, uint32(body.Len()))
	w.Write(body.Bytes())
	return w.Bytes()
}

func writeVarUint32(w *bytes.Buffer, v uint32) {
	var tmp [binary.MaxVarintLen32]byte
	n := 0
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		tmp[n] = b
		n++
		if v == 0 {
			break
		}
	}
	w.Write(tmp[:n])
}

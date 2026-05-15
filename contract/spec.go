package contract

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Spec wraps a Soroban contract's parsed spec entries (function signatures,
// struct/enum/union/error-enum schemas) and exposes name-keyed lookups used by
// the higher-level Client to validate calls, marshal arguments, and decode
// results. It mirrors the JS SDK's contract.Spec; marshaling helpers
// (NativeToScVal, FuncArgsToScVals, etc.) arrive in subsequent phases.
//
// A Spec is read-only after construction and safe to share across goroutines.
type Spec struct {
	entries []xdr.ScSpecEntry
	// byName indexes entries that have a "name" (functions and UDTs) for O(1)
	// lookup. Non-named entries (e.g. events do not have a name field in the
	// same shape) are omitted from this index.
	byName map[string]int
}

// wasmSpecCustomSectionName is the name of the WebAssembly custom section that
// holds the XDR-encoded stream of ScSpecEntry records emitted by the soroban
// SDK build pipeline.
const wasmSpecCustomSectionName = "contractspecv0"

// NewSpecFromEntries builds a Spec directly from a slice of decoded entries.
// The slice is retained by reference; callers must not mutate it afterwards.
func NewSpecFromEntries(entries []xdr.ScSpecEntry) *Spec {
	s := &Spec{
		entries: entries,
		byName:  make(map[string]int, len(entries)),
	}
	for i, e := range entries {
		if name, ok := entryName(e); ok {
			// First occurrence wins; duplicates would be a malformed spec, but
			// we tolerate it silently here and let downstream validation (T2.3)
			// surface it.
			if _, exists := s.byName[name]; !exists {
				s.byName[name] = i
			}
		}
	}
	return s
}

// NewSpecFromBase64 decodes a base64-encoded stream of XDR ScSpecEntry records
// and returns a Spec. This is the format produced when a spec is embedded in a
// Go source file via codegen.
func NewSpecFromBase64(b64 string) (*Spec, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("contract: decoding spec base64: %w", err)
	}
	entries, err := readSpecEntryStream(raw)
	if err != nil {
		return nil, err
	}
	return NewSpecFromEntries(entries), nil
}

// NewSpecFromWasm extracts the "contractspecv0" custom section from a
// Soroban contract's WASM binary and returns a parsed Spec. It returns an
// error if the binary is not a valid WebAssembly module or does not embed a
// contract spec.
func NewSpecFromWasm(wasm []byte) (*Spec, error) {
	payload, err := wasmCustomSection(wasm, wasmSpecCustomSectionName)
	if err != nil {
		return nil, fmt.Errorf("contract: extracting spec from wasm: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("contract: wasm has no %q custom section", wasmSpecCustomSectionName)
	}
	entries, err := readSpecEntryStream(payload)
	if err != nil {
		return nil, err
	}
	return NewSpecFromEntries(entries), nil
}

// Entries returns the underlying slice of spec entries in declaration order.
// The returned slice is the Spec's internal storage; callers must not mutate
// it.
func (s *Spec) Entries() []xdr.ScSpecEntry {
	if s == nil {
		return nil
	}
	return s.entries
}

// Funcs returns every function declared by the spec, in declaration order.
func (s *Spec) Funcs() []xdr.ScSpecFunctionV0 {
	if s == nil {
		return nil
	}
	out := make([]xdr.ScSpecFunctionV0, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Kind == xdr.ScSpecEntryKindScSpecEntryFunctionV0 && e.FunctionV0 != nil {
			out = append(out, *e.FunctionV0)
		}
	}
	return out
}

// HasFunc reports whether the spec defines a function with the given name.
// Layer-A clients use this to validate method names at Invoke time before
// paying for a network round-trip.
func (s *Spec) HasFunc(name string) bool {
	if s == nil {
		return false
	}
	i, ok := s.byName[name]
	if !ok {
		return false
	}
	return s.entries[i].Kind == xdr.ScSpecEntryKindScSpecEntryFunctionV0
}

// ErrorCases returns every case declared across all error-enum (#[contracterror])
// UDTs in the spec, in declaration order. Multiple error enums are flattened
// into a single slice — Soroban contracts conventionally declare one.
func (s *Spec) ErrorCases() []xdr.ScSpecUdtErrorEnumCaseV0 {
	if s == nil {
		return nil
	}
	var out []xdr.ScSpecUdtErrorEnumCaseV0
	for _, e := range s.entries {
		if e.Kind == xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0 && e.UdtErrorEnumV0 != nil {
			out = append(out, e.UdtErrorEnumV0.Cases...)
		}
	}
	return out
}

// entryName returns the declared name of an entry, when the entry kind has a
// name field. Function and UDT entries have names; events are excluded since
// they are not referenced by name at the call site.
func entryName(e xdr.ScSpecEntry) (string, bool) {
	switch e.Kind {
	case xdr.ScSpecEntryKindScSpecEntryFunctionV0:
		if e.FunctionV0 != nil {
			return string(e.FunctionV0.Name), true
		}
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		if e.UdtStructV0 != nil {
			return e.UdtStructV0.Name, true
		}
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		if e.UdtUnionV0 != nil {
			return e.UdtUnionV0.Name, true
		}
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		if e.UdtEnumV0 != nil {
			return e.UdtEnumV0.Name, true
		}
	case xdr.ScSpecEntryKindScSpecEntryUdtErrorEnumV0:
		if e.UdtErrorEnumV0 != nil {
			return e.UdtErrorEnumV0.Name, true
		}
	}
	return "", false
}

// readSpecEntryStream decodes a back-to-back stream of XDR-encoded
// ScSpecEntry records until the buffer is exhausted.
func readSpecEntryStream(buf []byte) ([]xdr.ScSpecEntry, error) {
	r := bytes.NewReader(buf)
	var entries []xdr.ScSpecEntry
	for r.Len() > 0 {
		var entry xdr.ScSpecEntry
		if _, err := xdr.Unmarshal(r, &entry); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("contract: truncated spec entry stream: %w", err)
			}
			return nil, fmt.Errorf("contract: decoding spec entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// wasmCustomSection extracts the payload of the first custom section with the
// given name from a WebAssembly module. It performs a minimal walk of the
// module structure — enough to locate custom sections — and does not validate
// the rest of the binary.
//
// See https://webassembly.github.io/spec/core/binary/modules.html#binary-module
// for the module format. Sections are encoded as (id u8)(size leb128)(bytes);
// section id 0 is "custom" and its payload starts with a name (name-length
// leb128 || bytes) followed by section-specific data.
func wasmCustomSection(wasm []byte, name string) ([]byte, error) {
	// Magic + version: "\0asm" + 0x01 0x00 0x00 0x00.
	if len(wasm) < 8 {
		return nil, fmt.Errorf("wasm too short")
	}
	if string(wasm[:4]) != "\x00asm" {
		return nil, fmt.Errorf("invalid wasm magic")
	}
	if !bytes.Equal(wasm[4:8], []byte{0x01, 0x00, 0x00, 0x00}) {
		return nil, fmt.Errorf("unsupported wasm version")
	}

	off := 8
	for off < len(wasm) {
		// Section id.
		id := wasm[off]
		off++
		// Section size (LEB128 unsigned).
		size, n, err := readVarUint32(wasm[off:])
		if err != nil {
			return nil, fmt.Errorf("reading section size: %w", err)
		}
		off += n
		end := off + int(size)
		if end > len(wasm) || end < off {
			return nil, fmt.Errorf("section length out of range")
		}
		if id == 0 {
			// Custom section: name then payload.
			body := wasm[off:end]
			nameLen, m, err := readVarUint32(body)
			if err != nil {
				return nil, fmt.Errorf("reading custom-section name length: %w", err)
			}
			if int(nameLen)+m > len(body) {
				return nil, fmt.Errorf("custom-section name overflows section")
			}
			sectionName := string(body[m : m+int(nameLen)])
			if sectionName == name {
				payload := body[m+int(nameLen):]
				// Return a copy so callers can mutate freely; cheap relative
				// to XDR decoding that follows.
				out := make([]byte, len(payload))
				copy(out, payload)
				return out, nil
			}
		}
		off = end
	}
	return nil, nil
}

// readVarUint32 decodes a WebAssembly LEB128-encoded unsigned 32-bit integer.
// It returns the value, the number of bytes consumed, and any error.
func readVarUint32(b []byte) (uint32, int, error) {
	var (
		val   uint32
		shift uint
	)
	for i := 0; i < len(b); i++ {
		c := b[i]
		val |= uint32(c&0x7f) << shift
		if c&0x80 == 0 {
			return val, i + 1, nil
		}
		shift += 7
		if shift >= 32 {
			return 0, 0, fmt.Errorf("varuint32 too long")
		}
	}
	return 0, 0, fmt.Errorf("unexpected end of varuint32")
}

package msmfw

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tsym struct {
	name       string
	value, sz  uint64
	shndx      uint16
	typ        byte
	secFlagged bool
}

// buildELF is a relocatable ELF64 with .rodata (data), a .bss and the symbols.
func buildELF(data []byte, syms []tsym) []byte {
	le := binary.LittleEndian
	strtab := []byte{0}
	symtab := make([]byte, 24)
	for _, s := range syms {
		off := len(strtab)
		strtab = append(append(strtab, s.name...), 0)
		e := make([]byte, 24)
		le.PutUint32(e[0:], uint32(off))
		e[4] = 1<<4 | s.typ // global
		le.PutUint16(e[6:], s.shndx)
		le.PutUint64(e[8:], s.value)
		le.PutUint64(e[16:], s.sz)
		symtab = append(symtab, e...)
	}
	shstr := []byte("\x00.rodata\x00.bss\x00.symtab\x00.strtab\x00.shstrtab\x00")
	nameOf := func(n string) uint32 { return uint32(bytes.Index(shstr, []byte("\x00"+n+"\x00")) + 1) }

	const ehsz = 64
	rodataOff := uint64(ehsz)
	strOff := rodataOff + uint64(len(data))
	symOff := strOff + uint64(len(strtab))
	shstrOff := symOff + uint64(len(symtab))
	shOff := shstrOff + uint64(len(shstr))

	out := make([]byte, ehsz)
	copy(out, "\x7fELF\x02\x01\x01")
	le.PutUint16(out[16:], uint16(elf.ET_REL))
	le.PutUint16(out[18:], uint16(elf.EM_AARCH64))
	le.PutUint32(out[20:], 1)
	le.PutUint64(out[40:], shOff)
	le.PutUint16(out[52:], ehsz)
	le.PutUint16(out[58:], 64)
	le.PutUint16(out[60:], 6)
	le.PutUint16(out[62:], 5)
	out = append(out, data...)
	out = append(out, strtab...)
	out = append(out, symtab...)
	out = append(out, shstr...)

	sh := func(name string, typ elf.SectionType, flags, off, size uint64, link, info uint32, entsize uint64) []byte {
		b := make([]byte, 64)
		le.PutUint32(b[0:], nameOf(name))
		le.PutUint32(b[4:], uint32(typ))
		le.PutUint64(b[8:], flags)
		le.PutUint64(b[24:], off)
		le.PutUint64(b[32:], size)
		le.PutUint32(b[40:], link)
		le.PutUint32(b[44:], info)
		le.PutUint64(b[48:], 1)
		le.PutUint64(b[56:], entsize)
		return b
	}
	out = append(out, make([]byte, 64)...)
	out = append(out, sh(".rodata", elf.SHT_PROGBITS, uint64(elf.SHF_ALLOC), rodataOff, uint64(len(data)), 0, 0, 0)...)
	out = append(out, sh(".bss", elf.SHT_NOBITS, uint64(elf.SHF_ALLOC), strOff, 64, 0, 0, 0)...)
	out = append(out, sh(".symtab", elf.SHT_SYMTAB, 0, symOff, uint64(len(symtab)), 4, 1, 24)...)
	out = append(out, sh(".strtab", elf.SHT_STRTAB, 0, strOff, uint64(len(strtab)), 0, 0, 0)...)
	out = append(out, sh(".shstrtab", elf.SHT_STRTAB, 0, shstrOff, uint64(len(shstr)), 0, 0, 0)...)
	return out
}

func writeELF(t *testing.T, data []byte, syms []tsym) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.ko")
	if err := os.WriteFile(p, buildELF(data, syms), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

const (
	rodata = 1
	bss    = 2
)

func TestElfSymbolRanges(t *testing.T) {
	data := []byte("0123456789ABCDEF")
	f := writeELF(t, data, []tsym{
		{name: "bit_code_319", value: 6, sz: 6, shndx: rodata},
		{name: "bit_code_111", value: 0, sz: 4, shndx: rodata},
		{name: "bit_code_size", value: 0, sz: 4, shndx: bss},         // no bytes in the file
		{name: "bit_code_abs", value: 4, sz: 4, shndx: 0xfff1},       // SHN_ABS
		{name: "bit_code_com", value: 3, sz: 3, shndx: 0xfff2},       // SHN_COMMON
		{name: "bit_code_und", value: 0, sz: 4, shndx: 0},            // undefined
		{name: "bit_code_big", value: 0, sz: 1 << 63, shndx: rodata}, // far past its section
		{name: "bit_code_over", value: 12, sz: 8, shndx: rodata},     // runs off the section
		{name: "bit_code_tls", value: 0, sz: 4, shndx: rodata, typ: byte(elf.STT_TLS)},
	})
	all, err := elfSymbolRanges(f, "bit_code_*")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].name != "bit_code_111" || all[1].name != "bit_code_319" {
		t.Fatalf("pattern = %+v", all)
	}
	// .rodata starts at the ELF header's end (64).
	if all[1].off != 64+6 || all[1].size != 6 {
		t.Errorf("bit_code_319 = %+v", all[1])
	}
	for _, n := range []string{"bit_code_size", "bit_code_abs", "bit_code_com", "bit_code_und", "bit_code_big", "bit_code_over", "bit_code_tls", "nope"} {
		if r, err := elfSymbolRanges(f, n); err == nil {
			t.Errorf("%s accepted: %+v", n, r)
		}
	}
	if _, err := elfSymbolRanges(f, "bit_[="); err == nil {
		t.Error("bad pattern accepted")
	}
}

func TestExtractReadBounds(t *testing.T) {
	data := []byte("0123456789ABCDEF")
	p := filepath.Join(t.TempDir(), "x.ko")
	os.WriteFile(p, buildELF(data, []tsym{{name: "s", value: 6, sz: 6, shndx: rodata}}), 0o644)

	sum := sha256.Sum256([]byte("6789AB"))
	ok := &Extract{Symbol: "s", Prefix: []byte("MX"), SHA256: sum, HasSHA: true}
	b, err := ok.read(p)
	if err != nil || len(b) != 1 || string(b[0].data) != "MX6789AB" || b[0].name != "s" {
		t.Fatalf("symbol read = %+v, %v", b, err)
	}
	wrong := &Extract{Symbol: "s", HasSHA: true}
	if _, err := wrong.read(p); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("wrong digest: %v", err)
	}
	// Huge, negative or overflowing ranges are refused before any allocation.
	for _, e := range []Extract{
		{Offset: 0, Length: 1 << 62},
		{Offset: 1 << 62, Length: 1 << 62},
		{Offset: 1<<63 - 1, Length: 2},
		{Offset: -1, Length: 4},
		{Offset: 0, Length: -4},
		{Offset: 0, Length: 1 << 40},
	} {
		e := e
		if b, err := e.read(p); err == nil {
			t.Errorf("%+v read %d blobs", e, len(b))
		}
	}
	// An offset range needs no ELF.
	raw := filepath.Join(t.TempDir(), "raw")
	os.WriteFile(raw, data, 0o644)
	b, err = (&Extract{Offset: 4, Length: 4}).read(raw)
	if err != nil || string(b[0].data) != "4567" {
		t.Errorf("raw range = %+v, %v", b, err)
	}
	// A symbol rule on a file that is not an ELF is an error, not a panic.
	if _, err := (&Extract{Symbol: "s"}).read(raw); err == nil {
		t.Error("non-ELF accepted")
	}
}

func TestParseRuleAtSign(t *testing.T) {
	// A file name containing '@' is written \@ and survives a round trip.
	r, err := ParseRule(`a\@2x.png=img/a.png`)
	if err != nil || r.Find != "a@2x.png" || r.Extract != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if r2, err := ParseRule(r.String()); err != nil || r2 != r {
		t.Errorf("round trip %q: %+v %v", r.String(), r2, err)
	}
	// ...and with an extraction after it.
	r, err = ParseRule(`a\@b.ko@sym=fw.bin`)
	if err != nil || r.Find != "a@b.ko" || r.Extract == nil || r.Extract.Symbol != "sym" {
		t.Fatalf("%+v %v", r, err)
	}
	if r2, err := ParseRule(r.String()); err != nil || r2.String() != r.String() {
		t.Errorf("round trip %q: %v", r.String(), err)
	}
	// Symbols may start with '.' or '$'; bad globs and empty options are errors.
	for _, ok := range []string{"x.ko@.LC0=a", "x.ko@$d=a", "x.ko@*_fw=d/"} {
		if _, err := ParseRule(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"x.ko@bit_[=d/", "x.ko@s=a?prefix", "x.ko@s=a?prefix=", "x.ko@ff+3=a", "x.ko@=a"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

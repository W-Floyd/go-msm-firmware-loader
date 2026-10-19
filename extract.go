package msmfw

import (
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
)

// blob is one extracted range: the symbol it came from (empty for an offset
// range) and its bytes, prefix included.
type blob struct {
	name string
	data []byte
}

// symRange is a symbol's name and where in the file its bytes are.
type symRange struct {
	name      string
	off, size int64
}

// read is the ranges (with their prefix) of the file at path, or an error
// when the file has none, a range is outside it, or a range's digest is not
// the expected one. Every range is checked against the file's size before
// anything is allocated: the file is a vendor's, not trusted.
func (e *Extract) read(path string) ([]blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	spans := []symRange{{"", e.Offset, e.Length}}
	if e.Symbol != "" {
		if spans, err = elfSymbolRanges(f, e.Symbol); err != nil {
			return nil, err
		}
	}
	var out []blob
	for _, s := range spans {
		if s.off < 0 || s.size <= 0 || s.off > fi.Size() || s.size > fi.Size()-s.off {
			return nil, fmt.Errorf("%s: range %d+%d is outside the %d-byte file", s.name, s.off, s.size, fi.Size())
		}
		buf := make([]byte, s.size)
		if _, err := f.ReadAt(buf, s.off); err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		if e.HasSHA && sha256.Sum256(buf) != e.SHA256 {
			return nil, errors.New("range has a different sha256")
		}
		out = append(out, blob{s.name, append(append([]byte{}, e.Prefix...), buf...)})
	}
	return out, nil
}

// elfSymbolRanges is the symbols matching pattern (a name, or a path.Match
// pattern) that have bytes in the file, in name order. A symbol counts only if
// it is defined in a real section (not undefined, absolute or common), is not
// thread-local, lies wholly inside its section, and the section is stored
// uncompressed. In a relocatable object (a .ko) a symbol's value is relative to
// its section; in a linked one it is an address.
func elfSymbolRanges(f *os.File, pattern string) ([]symRange, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return nil, err
	}
	syms, err := ef.Symbols()
	if err != nil {
		return nil, err
	}
	var out []symRange
	seen := map[string]bool{}
	for _, s := range syms {
		if s.Name == "" || s.Size == 0 || seen[s.Name] || elf.ST_TYPE(s.Info) == elf.STT_TLS {
			continue
		}
		if ok, _ := path.Match(pattern, s.Name); !ok {
			continue
		}
		if s.Section == elf.SHN_UNDEF || s.Section >= elf.SHN_LORESERVE || int(s.Section) >= len(ef.Sections) {
			continue
		}
		sec := ef.Sections[s.Section]
		if sec.Type == elf.SHT_NOBITS || sec.Flags&elf.SHF_COMPRESSED != 0 {
			continue
		}
		rel := s.Value
		if ef.Type != elf.ET_REL {
			if s.Value < sec.Addr {
				continue
			}
			rel = s.Value - sec.Addr
		}
		if rel > sec.Size || s.Size > sec.Size-rel {
			continue
		}
		off := sec.Offset + rel
		if off < sec.Offset || off > 1<<62 || s.Size > 1<<62 {
			continue
		}
		seen[s.Name] = true
		out = append(out, symRange{s.Name, int64(off), int64(s.Size)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no symbol %s with bytes", pattern)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

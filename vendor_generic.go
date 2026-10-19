package msmfw

import (
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// genericVendor is driven by Config.Rules: every named partition is mounted
// (read-only) unless Config.Partitions names some, and each rule's file is
// found in them and placed in the target. It knows nothing of any family; a
// vendor that does (qcom) is quicker and cleverer, and this is for the device
// none knows yet.
type genericVendor struct{}

func (genericVendor) Name() string { return "generic" }

func (genericVendor) Validate(cfg Config) error {
	if cfg.Compat {
		return errors.New("generic vendor: Compat is the Qualcomm script's")
	}
	if len(cfg.Rules) == 0 {
		return errors.New("generic vendor: no rules (Config.Rules, --rule)")
	}
	return nil
}

// Partitions is every named partition: rules look everywhere.
func (genericVendor) Partitions(Config) []string { return nil }

// SuperPartitions: an Android device's dynamic partitions are looked for too,
// so rules reach into them as into any other partition.
func (genericVendor) SuperPartitions(Config) []string { return DefaultSuperPartitions() }

// CopyCapable: the files are placed through Env.Place.
func (genericVendor) CopyCapable() {}

func (genericVendor) Link(env *Env) error {
	cfg := env.Config()
	env.LinkExtra()
	// One walk of each partition, in mount order.
	var parts [][]string
	for _, m := range env.Mounted() {
		parts = append(parts, env.Walk(m, cfg.MaxDepth))
	}
	for _, rule := range cfg.Rules {
		n := 0
		seen := map[string]bool{}
	search:
		for _, files := range parts {
			for _, f := range files {
				base := filepath.Base(f)
				if !rule.matches(base) || seen[base] {
					continue
				}
				if rule.Extract != nil {
					blobs, err := rule.Extract.read(f)
					if err != nil {
						env.Logf("rule %s: %s: %v", rule, strings.TrimPrefix(f, env.Base()+"/"), err)
						continue
					}
					from := strings.TrimPrefix(f, env.Base()+"/")
					for _, b := range blobs {
						dst := rule.Dst
						if rule.isDir() {
							dst = pathJoin(dst, b.name)
						}
						if env.PlaceBytes(b.data, dst, from+"@"+b.name) {
							n++
						}
					}
					break search
				}
				seen[base] = true
				dst := rule.Dst
				if rule.isDir() {
					dst = pathJoin(dst, base)
				}
				env.Place(f, dst)
				n++
				if !rule.isDir() {
					break search
				}
			}
		}
		if n == 0 {
			env.Logf("rule %s: no file found", rule)
			env.Missing(rule)
		}
	}
	return nil
}

// blob is one extracted range: the symbol it came from (empty for an offset
// range) and its bytes, prefix included.
type blob struct {
	name string
	data []byte
}

// read is the ranges (with their prefix) of the file at path, or an error
// when the file has none, is too short, or a range's digest is not the
// expected one.
func (e *Extract) read(path string) ([]blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type span struct {
		name      string
		off, size int64
	}
	spans := []span{{"", e.Offset, e.Length}}
	if e.Symbol != "" {
		syms, err := elfSymbolRanges(f, e.Symbol)
		if err != nil {
			return nil, err
		}
		spans = spans[:0]
		for _, s := range syms {
			spans = append(spans, span(s))
		}
	}
	var out []blob
	for _, s := range spans {
		buf := make([]byte, s.size)
		if _, err := f.ReadAt(buf, s.off); err != nil {
			return nil, fmt.Errorf("%s: range is outside the file", s.name)
		}
		if e.HasSHA && sha256.Sum256(buf) != e.SHA256 {
			return nil, errors.New("range has a different sha256")
		}
		out = append(out, blob{s.name, append(append([]byte{}, e.Prefix...), buf...)})
	}
	return out, nil
}

// symRange is a symbol's name and where in the file its bytes are.
type symRange struct {
	name      string
	off, size int64
}

// elfSymbolRanges is the symbols matching pattern (a name, or a path.Match
// pattern) that have bytes in the file, in name order. In a relocatable
// object (a .ko) a symbol's value is relative to its section; in a linked one
// it is an address, found in a section by range.
func elfSymbolRanges(f *os.File, pattern string) ([]symRange, error) {
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
		if ok, _ := path.Match(pattern, s.Name); !ok || s.Size == 0 || seen[s.Name] {
			continue
		}
		var sec *elf.Section
		var off int64
		if ef.Type == elf.ET_REL && int(s.Section) < len(ef.Sections) {
			sec = ef.Sections[s.Section]
			off = int64(sec.Offset + s.Value)
		} else {
			for _, c := range ef.Sections {
				if c.Type != elf.SHT_NOBITS && s.Value >= c.Addr && s.Value < c.Addr+c.Size {
					sec, off = c, int64(c.Offset+s.Value-c.Addr)
				}
			}
		}
		if sec == nil || sec.Type == elf.SHT_NOBITS {
			continue
		}
		seen[s.Name] = true
		out = append(out, symRange{s.Name, off, int64(s.Size)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no symbol %s with bytes", pattern)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

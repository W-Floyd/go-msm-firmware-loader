package msmfw

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Rules are the generic vendor's configuration (vendor_generic.go): which
// files to bring out of the partitions that hold them, and under which names
// the kernel's drivers ask for them. Nothing in them is about one SoC, so a
// device whose layout no built-in vendor knows can be handled, or a vendor
// tried out, with no code:
//
//	Rule{Find: "fw_bcmdhd.bin", Dst: "brcm/brcmfmac4359-sdio.bin"}
//
// finds fw_bcmdhd.bin anywhere (to MaxDepth) in the partitions and makes it
// appear as brcm/brcmfmac4359-sdio.bin in the directory the kernel is pointed
// at. A built-in vendor, which knows its family's partitions and where the
// files are in them, is quicker and cleverer; rules are the way that needs no
// knowledge.

// DefaultMaxDepth is how deep below a partition's root a rule looks.
const DefaultMaxDepth = 6

// Rule places one firmware file, or a set of them.
type Rule struct {
	// Find is a file name, or a path.Match pattern, matched against the base
	// name of every regular file in the mounted partitions (symbolic links
	// are not followed). The first partition, in the order they are
	// mounted, that has a match wins; within one, the first in name order.
	Find string
	// Dst is where the file appears under the target directory, a relative
	// path whose directories are made. A Dst that ends in "/" is a directory
	// instead: every distinct file name Find matches goes in it, under its
	// own name.
	Dst string
	// Extract, when set, carves a byte range out of the file Find matches
	// instead of placing the file: for a blob that exists only inside a
	// vendor binary (firmware embedded in a kernel module).
	Extract *Extract
}

// Extract is a byte range of the matched file, written out as Dst.
type Extract struct {
	// Symbol, when set, names an ELF symbol (a kernel module's firmware
	// array) whose bytes are the range: its place and size come from the
	// file's own symbol table, so a rebuilt binary needs no new offsets. A
	// path.Match pattern (bit_code_*) takes every symbol it matches, each as
	// a file under a directory Dst named after the symbol.
	Symbol         string
	Offset, Length int64
	// Prefix is written before the range (a header the driver wants and the
	// vendor's copy lacks).
	Prefix []byte
	// SHA256, when HasSHA, is the digest the range must have (before the
	// prefix); a candidate that differs is passed over, so a different build
	// of the binary is not installed under the right name.
	SHA256 [sha256.Size]byte
	HasSHA bool
}

// ParseRule reads "FIND=DST", or "FIND@SYMBOL=DST[?prefix=HEX&sha256=HEX]" /
// "FIND@OFFSET+LENGTH=DST[...]" to extract an ELF symbol's bytes, or a byte
// range (decimal or 0x), instead of placing the file.
func ParseRule(s string) (Rule, error) {
	find, dst, ok := strings.Cut(s, "=")
	find, dst = strings.TrimSpace(find), strings.TrimSpace(dst)
	if !ok || find == "" || dst == "" {
		return Rule{}, fmt.Errorf("rule %q: want FIND=DST", s)
	}
	var ex *Extract
	if i := strings.LastIndex(find, "@"); i >= 0 {
		spec := find[i+1:]
		find = find[:i]
		ex = &Extract{}
		if c := spec[:min(1, len(spec))]; c == "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") {
			ex.Symbol = spec
		} else {
			off, length, ok := strings.Cut(spec, "+")
			var err1, err2 error
			ex.Offset, err1 = strconv.ParseInt(off, 0, 64)
			ex.Length, err2 = strconv.ParseInt(length, 0, 64)
			if !ok || err1 != nil || err2 != nil || ex.Offset < 0 || ex.Length <= 0 {
				return Rule{}, fmt.Errorf("rule %q: want FIND@SYMBOL or FIND@OFFSET+LENGTH", s)
			}
		}
		dst, opts, _ := strings.Cut(dst, "?")
		for _, kv := range strings.FieldsFunc(opts, func(r rune) bool { return r == '&' }) {
			k, v, _ := strings.Cut(kv, "=")
			b, err := hex.DecodeString(v)
			switch {
			case err != nil:
				return Rule{}, fmt.Errorf("rule %q: %s: %w", s, k, err)
			case k == "prefix":
				ex.Prefix = b
			case k == "sha256" && len(b) == sha256.Size:
				copy(ex.SHA256[:], b)
				ex.HasSHA = true
			default:
				return Rule{}, fmt.Errorf("rule %q: bad option %q", s, kv)
			}
		}
		glob := ex.Symbol != "" && strings.ContainsAny(ex.Symbol, "*?[")
		if strings.HasSuffix(dst, "/") != glob {
			return Rule{}, fmt.Errorf("rule %q: a symbol pattern needs a directory DST (ending in /), anything else a file", s)
		}
		if glob && ex.HasSHA {
			return Rule{}, fmt.Errorf("rule %q: sha256 names one range, not a pattern", s)
		}
		r, err := ParseRule(find + "=" + dst)
		if err != nil {
			return Rule{}, err
		}
		r.Extract = ex
		return r, nil
	}
	if _, err := path.Match(find, ""); err != nil {
		return Rule{}, fmt.Errorf("rule %q: %w", s, err)
	}
	if strings.Contains(find, "/") {
		return Rule{}, fmt.Errorf("rule %q: FIND is a file name, not a path", s)
	}
	dirDst := strings.HasSuffix(dst, "/")
	clean := path.Clean(dst)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return Rule{}, fmt.Errorf("rule %q: DST must stay inside the target directory", s)
	}
	if dirDst {
		clean += "/"
	}
	return Rule{Find: find, Dst: clean}, nil
}

func (r Rule) String() string {
	e := r.Extract
	if e == nil {
		return r.Find + "=" + r.Dst
	}
	out := fmt.Sprintf("%s@%s=%s", r.Find, e.Symbol, r.Dst)
	if e.Symbol == "" {
		out = fmt.Sprintf("%s@%d+%d=%s", r.Find, e.Offset, e.Length, r.Dst)
	}
	var opts []string
	if len(e.Prefix) > 0 {
		opts = append(opts, "prefix="+hex.EncodeToString(e.Prefix))
	}
	if e.HasSHA {
		opts = append(opts, "sha256="+hex.EncodeToString(e.SHA256[:]))
	}
	if len(opts) > 0 {
		out += "?" + strings.Join(opts, "&")
	}
	return out
}

func (r Rule) isDir() bool { return strings.HasSuffix(r.Dst, "/") }

func (r Rule) matches(name string) bool {
	ok, _ := path.Match(r.Find, name)
	return ok
}

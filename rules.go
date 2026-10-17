package msmfw

import (
	"fmt"
	"path"
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
}

// ParseRule reads "FIND=DST".
func ParseRule(s string) (Rule, error) {
	find, dst, ok := strings.Cut(s, "=")
	find, dst = strings.TrimSpace(find), strings.TrimSpace(dst)
	if !ok || find == "" || dst == "" {
		return Rule{}, fmt.Errorf("rule %q: want FIND=DST", s)
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

func (r Rule) String() string { return r.Find + "=" + r.Dst }

func (r Rule) isDir() bool { return strings.HasSuffix(r.Dst, "/") }

func (r Rule) matches(name string) bool {
	ok, _ := path.Match(r.Find, name)
	return ok
}

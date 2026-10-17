package msmfw

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The vendor seam. Run does what is the same for every device: find the
// partitions in sysfs, mount them read-only (the partitions of Android's
// "super" through a loop device or device-mapper), unfold them under
// Base/mnt, and point the kernel's firmware_class.path at Base/target. What
// differs is which partitions hold firmware, and how the blobs in them are
// made to appear in the target under the names the drivers ask for. That is a
// Vendor.
//
// Two are built in: "qcom", the Qualcomm linking of postmarketOS's
// msm-firmware-loader (the default), and "generic", driven by Config.Rules.
// Another vendor is a type that implements Vendor, and Register.

// Vendor is one family of devices' way of presenting firmware.
type Vendor interface {
	// Name is what Config.Vendor and --vendor call it.
	Name() string
	// Validate is for options that do not fit this vendor (rules for one that
	// takes none, say); an error stops Run before anything is mounted.
	Validate(cfg Config) error
	// Partitions is the names of the partitions to take firmware from, exactly
	// as they are on the device. A nil list is every named partition, in the
	// order the disks list them. Config.Partitions, when set, replaces it.
	Partitions(cfg Config) []string
	// Link builds the target from the mounted partitions, with the helpers
	// of env.
	Link(env *Env) error
}

// SuperProvider is a Vendor with dynamic partitions to look for (Android's
// "super"); Config.SuperPartitions, when set, replaces it. A vendor without it
// has none looked for beyond what the generic partition list matches.
type SuperProvider interface {
	SuperPartitions(cfg Config) []string
}

// PartitionChecker is a Vendor that can say what is wrong with the disks it
// is about to be run on (a missing A/B suffix, say), once, before mounting.
type PartitionChecker interface {
	CheckPartitions(cfg Config, names []string, logf func(string, ...any))
}

// CopyCapable is a Vendor whose Link places files through Env.Place, so
// Config.Copy (copies, and the partitions let go) works with it.
type CopyCapable interface {
	CopyCapable()
}

var (
	vendorMu sync.RWMutex
	vendors  = map[string]Vendor{}
)

// Register adds a vendor, replacing one of the same name. Run and the command
// line then take it by name.
func Register(v Vendor) {
	vendorMu.Lock()
	defer vendorMu.Unlock()
	vendors[v.Name()] = v
}

// Vendors are the registered names, sorted.
func Vendors() []string {
	vendorMu.RLock()
	defer vendorMu.RUnlock()
	var out []string
	for n := range vendors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LookupVendor is the vendor of that name.
func LookupVendor(name string) (Vendor, bool) {
	vendorMu.RLock()
	defer vendorMu.RUnlock()
	v, ok := vendors[name]
	return v, ok
}

func init() {
	Register(qcomVendor{})
	Register(genericVendor{})
}

// vendorFor is the vendor a Config means: the named one, else "generic" when
// there are rules, else "qcom", which is what the package was.
func vendorFor(cfg Config) (Vendor, error) {
	name := cfg.Vendor
	if name == "" {
		name = "qcom"
		if len(cfg.Rules) > 0 {
			name = "generic"
		}
	}
	v, ok := LookupVendor(name)
	if !ok {
		return nil, fmt.Errorf("no vendor %q (have %s)", name, strings.Join(Vendors(), ", "))
	}
	return v, nil
}

// Env is what a Vendor's Link works with: the unfolded partitions and the
// target, and helpers that make a file appear in the target (a link, or a
// copy when Config.Copy is set).
type Env struct{ r *runner }

// Config is the configuration of this run.
func (e *Env) Config() Config { return e.r.cfg }

// Base is the directory the partitions are unfolded into, Target the
// directory the kernel is pointed at, and MountDir the directory under Base
// that partition name is mounted on.
func (e *Env) Base() string   { return e.r.res.Base }
func (e *Env) Target() string { return e.r.res.Target }
func (e *Env) MountDir(name string) string {
	return filepath.Join(e.r.res.Base, "mnt", name)
}

// Mounted are the partitions that mounted, in order (those that did not are
// in Result.Mounts with their error).
func (e *Env) Mounted() []Mount {
	var out []Mount
	for _, m := range e.r.res.Mounts {
		if m.Err == nil {
			out = append(out, m)
		}
	}
	return out
}

// Logf logs through Config.Logf.
func (e *Env) Logf(format string, args ...any) { e.r.cfg.Logf(format, args...) }

// Walk is the regular files below a mounted partition, at most depth
// directories down (DefaultMaxDepth when depth is not positive), in lexical
// order, links not followed.
func (e *Env) Walk(m Mount, depth int) []string {
	if depth <= 0 {
		depth = DefaultMaxDepth
	}
	return walkFiles(m.Dir, depth)
}

// Place makes src, a file under a mounted partition, appear at dst, a
// relative path under the target whose directories are made: as a link, or
// as a copy with Config.Copy. A name already there stays, and Place says
// false. It counts into Result.Links or Result.Copied.
func (e *Env) Place(src, dst string) bool { return e.r.place(src, dst) }

// LinkExtra links the entries of the directory the kernel was already using
// (Config.ExtraPath) into the target, so what it had keeps loading.
func (e *Env) LinkExtra() {
	if e.r.cfg.ExtraPath != "" {
		e.r.linkExtra()
	}
}

// Missing records a want that found nothing, in Result.Missing.
func (e *Env) Missing(r Rule) { e.r.res.Missing = append(e.r.res.Missing, r) }

// place is Env.Place.
func (r *runner) place(src, dst string) bool {
	full := filepath.Join(r.res.Target, filepath.FromSlash(dst))
	if _, err := os.Lstat(full); err == nil {
		r.cfg.Logf("%s: already there, kept", dst)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.cfg.Logf("%s: %v", dst, err)
		return false
	}
	if r.cfg.Copy {
		if err := copyFile(src, full); err != nil {
			r.cfg.Logf("copy %s: %v", dst, err)
			os.Remove(full)
			return false
		}
		r.res.Copied++
		r.cfg.Logf("%s <- %s (copied)", dst, strings.TrimPrefix(src, r.res.Base+"/"))
		return true
	}
	if err := os.Symlink(src, full); err != nil {
		r.cfg.Logf("ln: %v", err)
		return false
	}
	r.res.Links++
	r.cfg.Logf("%s -> %s", dst, strings.TrimPrefix(src, r.res.Base+"/"))
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// linkExtra is Env.LinkExtra.
func (r *runner) linkExtra() {
	ents, err := os.ReadDir(r.cfg.ExtraPath)
	if err != nil {
		r.cfg.Logf("extra path %s: %v", r.cfg.ExtraPath, err)
		return
	}
	for _, e := range ents {
		src := filepath.Join(r.cfg.ExtraPath, e.Name())
		dst := filepath.Join(r.res.Target, e.Name())
		if err := os.Symlink(src, dst); err != nil && !errors.Is(err, fs.ErrExist) {
			r.cfg.Logf("ln: %v", err)
			continue
		}
		r.res.Links++
	}
}

// releaseMounts unmounts the partitions (and their loop or mapper devices),
// keeping the tmpfs and the target: for copies, which need nothing from them
// afterwards.
func (res *Result) releaseMounts() {
	for i := len(res.Mounts) - 1; i >= 0; i-- {
		m := &res.Mounts[i]
		if m.Err != nil || m.released {
			continue
		}
		if err := res.cfg.System.Unmount(m.Dir); err != nil {
			res.cfg.Logf("unmount %s: %v", m.Dir, err)
			continue
		}
		if m.dm != "" {
			res.cfg.System.DMRemove(m.dm)
			m.dm = ""
		}
		m.released = true
	}
}

// walkFiles is the regular files below dir, at most depth directories down,
// in lexical order, without following links.
func walkFiles(dir string, depth int) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is skipped, not fatal
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= depth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// pathJoin is path.Join, for the slash-separated destinations.
func pathJoin(elem ...string) string { return path.Join(elem...) }

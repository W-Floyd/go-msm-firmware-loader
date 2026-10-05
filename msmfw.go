// Package msmfw collects the firmware blobs a Qualcomm SoC's remote
// processors need from the vendor partitions that already hold them, and
// presents them as one directory for the kernel's firmware loader
// (firmware_class.path).
//
// It is a static Go port of postmarketOS's msm-firmware-loader shell script,
// for a system that has none of that script's helpers: no udev (so no
// /dev/disk/by-partlabel), no qbootctl, no device-mapper tools, no busybox.
// Partitions are found by their name in sysfs, the A/B slot comes from the
// kernel command line, and a logical partition of the "super" partition is
// mounted through a loop device over its extent, with device-mapper only as
// the fallback for one in several pieces.
//
// The package has no process-level side effects: it reads no flags and
// environment, writes no os.Stdout, never exits, and keeps no globals. Run
// can be called from a long-lived process, and again after Result.Close.
package msmfw

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
	"github.com/W-Floyd/go-msm-firmware-loader/internal/sys"
)

// DefaultBase is where the script unfolds the partitions.
const DefaultBase = "/run/msm-firmware-loader"

// Extent is a run of 512-byte sectors on the super device.
type Extent = liblp.Extent

// System is the privileged work Run delegates, so a test (or a caller with
// its own mount code) can replace it. NewSystem is the real one.
type System interface {
	MountTmpfs(dir string) error
	// MountRO mounts a block device read-only, finding its file system.
	MountRO(dev, dir string) error
	Unmount(dir string) error
	// LoopOver makes a read-only loop device over a byte range of backing.
	// release drops the caller's hold once the device is mounted (or the
	// mount failed); the loop device then goes with its last user.
	LoopOver(backing string, off, size uint64) (dev string, release func(), err error)
	HasDM() bool
	DMCreate(superDev, name string, ex []Extent) (dev string, err error)
	DMRemove(name string) error
}

// NewSystem is the System that uses the kernel directly, for the given /dev
// and /proc.
func NewSystem(dev, proc string) System { return sys.System{Dev: dev, Proc: proc} }

// Config says what to load and where things are. The zero value, with the
// defaults it implies, is the script's behaviour on a running system, except
// that SlotSuffix is empty (see SlotSuffixFromCmdline).
type Config struct {
	// Base is the directory the partitions are unfolded into: a tmpfs is
	// mounted there with mnt/ and target/ below. Default DefaultBase.
	Base string

	// SlotSuffix is the active A/B slot's, "_a", or "" on a non-A/B device.
	// It is appended to the default partition names, and picks the
	// dynamic-partition metadata copy (a is 0, b is 1).
	SlotSuffix string

	// Partitions replaces the list of partitions to mount, names exactly as
	// they are on the device (so with any slot suffix). Nil is the script's
	// list for SlotSuffix. SuperPartitions likewise replaces "super",
	// "system".
	Partitions      []string
	SuperPartitions []string

	// ExactMatch makes a partition name match only a whole entry of the
	// lists. The script matches by substring, so a partition "sp" is
	// mounted because "dsp_a" contains it; that is the default here, to
	// load what the script does.
	ExactMatch bool

	// ExtraPath is a directory of blobs to link first, so that they win
	// over the partitions': the script uses the current firmware_class.path.
	// Empty means none; use CurrentFirmwarePath for the script's.
	ExtraPath string

	// NoDM never uses device-mapper: a partition in several extents is
	// reported and left unmounted.
	NoDM bool
	// NoSetPath leaves firmware_class.path alone.
	NoSetPath bool
	// DryRun reads sysfs and the super metadata, and records in the Result
	// what would be mounted, but changes nothing.
	DryRun bool

	// Roots of the system, for tests; the defaults are the real ones.
	Sys  string // /sys
	Dev  string // /dev
	Proc string // /proc

	// Logf receives progress and the errors that are carried on from, as
	// the script's stderr would. Nil discards.
	Logf func(format string, args ...any)

	// System replaces the real mounts. Nil is NewSystem(Dev, Proc).
	System System
}

func (c *Config) setDefaults() {
	if c.Base == "" {
		c.Base = DefaultBase
	}
	c.Base = filepath.Clean(c.Base)
	for p, d := range map[*string]string{&c.Sys: "/sys", &c.Dev: "/dev", &c.Proc: "/proc"} {
		if *p == "" {
			*p = d
		}
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	if c.System == nil {
		c.System = NewSystem(c.Dev, c.Proc)
	}
}

// DefaultPartitions is the script's list of partitions to take blobs from
// (with the fsg partition our fork adds for tqftpserv).
func DefaultPartitions(slotSuffix string) []string {
	s := slotSuffix
	return []string{"apnhlos", "bluetooth" + s, "dsp" + s, "fsg" + s, "modem" + s, "persist", "vendor" + s}
}

// DefaultSuperPartitions is where the script looks for dynamic partitions.
func DefaultSuperPartitions() []string { return []string{"super", "system"} }

// SlotSuffixFromCmdline is androidboot.slot_suffix from a kernel command
// line, the script's fallback when qbootctl is not there.
func SlotSuffixFromCmdline(cmdline string) string {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, "androidboot.slot_suffix="); ok {
			return v
		}
	}
	return ""
}

// CurrentFirmwarePath is the kernel's firmware_class.path now, "" if unset.
func CurrentFirmwarePath(sysRoot string) string {
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	b, _ := os.ReadFile(filepath.Join(sysRoot, "module/firmware_class/parameters/path"))
	return strings.TrimRight(string(b), "\n\x00")
}

// Mount is one partition Run mounted, or tried to.
type Mount struct {
	// Name is the partition's name, and the directory's under Base/mnt.
	Name string
	Dir  string
	// Device is what was mounted: a block device or loop or mapper node;
	// in a dry run, a description.
	Device string
	// Via is how Device was made: "direct" (a partition of its own), "loop"
	// (one extent of super) or "dm" (several).
	Via string
	// Err is why it is not mounted. The script carries on past these.
	Err error

	dm string // the device-mapper name to remove
}

// Result is what Run did.
type Result struct {
	Base   string
	Target string // the directory of links, Base/target
	Mounts []Mount
	// Links is how many symlinks were made in the target. Links that could
	// not be made (already there) are logged, not counted.
	Links int
	// PathSet is whether firmware_class.path was written.
	PathSet bool
	DryRun  bool

	cfg    Config
	closed bool
}

// Close undoes what Run mounted: the partitions, their loop and mapper
// devices, and the tmpfs, which takes the links with it. It is for tests and
// for a caller that has what it needs; the usual thing is to leave it all
// mounted. If firmware_class.path was set to the target, it is left pointing
// at nothing: the caller that wants it unset must write it itself.
func (r *Result) Close() error {
	if r == nil || r.closed || r.DryRun {
		return nil
	}
	r.closed = true
	var errs []error
	for i := len(r.Mounts) - 1; i >= 0; i-- {
		m := r.Mounts[i]
		if m.Err == nil {
			if err := r.cfg.System.Unmount(m.Dir); err != nil {
				errs = append(errs, fmt.Errorf("unmount %s: %w", m.Dir, err))
			}
		}
		if m.dm != "" {
			if err := r.cfg.System.DMRemove(m.dm); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", m.dm, err))
			}
		}
	}
	if err := r.cfg.System.Unmount(r.Base); err != nil {
		errs = append(errs, fmt.Errorf("unmount %s: %w", r.Base, err))
	}
	return errors.Join(errs...)
}

// Teardown unmounts everything mounted at or under base, from mountinfo, for
// a process that did not keep its Result (the loader exits and leaves its
// mounts). Loop devices free themselves with their mounts.
func Teardown(base, proc string) error {
	if proc == "" {
		proc = "/proc"
	}
	mps, err := sys.MountPoints(filepath.Join(proc, "self/mountinfo"), filepath.Clean(base))
	if err != nil {
		return err
	}
	var errs []error
	s := sys.System{}
	for _, mp := range mps {
		if err := s.Unmount(mp); err != nil {
			errs = append(errs, fmt.Errorf("unmount %s: %w", mp, err))
		}
	}
	return errors.Join(errs...)
}

// ErrLoaded is returned by Run when Base is already a mount point, from an
// earlier Run or the script.
var ErrLoaded = errors.New("already loaded")

func isMountPoint(proc, dir string) bool {
	mps, err := sys.MountPoints(filepath.Join(proc, "self/mountinfo"), dir)
	if err != nil {
		return false
	}
	return slicesContains(mps, dir)
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// logWriter adapts an io.Writer to a Logf, with a prefix.
func logWriter(w io.Writer, prefix string) func(string, ...any) {
	return func(f string, a ...any) { fmt.Fprintf(w, prefix+f+"\n", a...) }
}

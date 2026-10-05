package msmfw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

// part is a partition as sysfs describes it.
type part struct{ devName, partName string }

// scanPartitions lists the partitions of eMMC/SD and UFS/SCSI disks that have
// a name, in the script's order (shell glob order, which is byte order).
// /dev/disk/by-partlabel does not exist yet when this runs; sysfs does.
func scanPartitions(sysRoot string) []part {
	var out []part
	for _, pat := range []string{"block/mmcblk*/mmcblk*p*", "block/sd*/sd*"} {
		paths, _ := filepath.Glob(filepath.Join(sysRoot, pat))
		for _, p := range paths {
			b, err := os.ReadFile(filepath.Join(p, "uevent"))
			if err != nil {
				continue
			}
			var pt part
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "DEVNAME="); ok && pt.devName == "" {
					pt.devName = v
				}
				if v, ok := strings.CutPrefix(line, "PARTNAME="); ok && pt.partName == "" {
					pt.partName = v
				}
			}
			out = append(out, pt)
		}
	}
	return out
}

// matcher is the script's test of a partition name against a list.
//
// The script asks whether the name is a SUBSTRING of the list written out as
// one string, which is how a partition "sp" gets mounted: "dsp_a" contains
// it. (The newline-and-tab layout is the script's too.) exact is the sane
// reading, a whole entry.
func matcher(list []string, exact bool) func(string) bool {
	if exact {
		return func(n string) bool { return n != "" && slicesContains(list, n) }
	}
	joined := "\n\t" + strings.Join(list, "\n\t") + "\n"
	return func(n string) bool { return n != "" && strings.Contains(joined, n) }
}

func slotIndex(suffix string) int {
	if len(suffix) == 2 && suffix[0] == '_' && suffix[1] >= 'a' && suffix[1] <= 'z' {
		return int(suffix[1] - 'a')
	}
	return 0
}

type runner struct {
	cfg     Config
	res     *Result
	claimed map[string]bool // names with a directory under mnt/, mounted or not
	isFW    func(string) bool
}

// Run unfolds the partitions under cfg.Base and links their blobs into
// Base/target, as msm-firmware-loader.sh does, then points the kernel at it
// (unless NoSetPath). A partition that cannot be mounted is logged and
// recorded in Result.Mounts, not an error: the script goes on past those.
// An error is only for what makes the rest pointless (no tmpfs, no
// directories, no way to set the path), and comes with a Result that can
// still be Closed.
func Run(cfg Config) (*Result, error) {
	cfg.setDefaults()
	if filepath.Clean(cfg.ExtraPath) == filepath.Join(cfg.Base, "target") {
		cfg.ExtraPath = "" // our own target from an earlier run would link to itself
	}
	res := &Result{Base: cfg.Base, Target: filepath.Join(cfg.Base, "target"), DryRun: cfg.DryRun, cfg: cfg}
	fw := cfg.Partitions
	if fw == nil {
		fw = DefaultPartitions(cfg.SlotSuffix)
	}
	super := cfg.SuperPartitions
	if super == nil {
		super = DefaultSuperPartitions()
	}
	exact := !(cfg.Compat || cfg.SubstringMatch) || cfg.ExactMatch
	r := &runner{cfg: cfg, res: res, claimed: map[string]bool{}, isFW: matcher(fw, exact)}

	if !cfg.DryRun {
		if err := r.prepareBase(); err != nil {
			if errors.Is(err, ErrLoaded) {
				return nil, err // not ours to Close
			}
			return res, err
		}
	}

	parts := scanPartitions(cfg.Sys)
	if cfg.SlotSuffix == "" && !cfg.Compat && cfg.Partitions == nil {
		for _, p := range parts {
			if strings.HasSuffix(p.partName, "_a") || strings.HasSuffix(p.partName, "_b") {
				cfg.Logf("warning: no slot suffix (androidboot.slot_suffix), but partition %q is A/B; the firmware partitions will not be found", p.partName)
				break
			}
		}
	}
	r.mountDynamic(parts, matcher(super, exact))
	for _, p := range parts {
		if r.isFW(p.partName) && p.devName != "" && !r.claimed[p.partName] {
			r.mount(p.partName, "direct", "/dev/"+p.devName, func() (string, func(), error) {
				return filepath.Join(cfg.Dev, p.devName), func() {}, nil
			})
		}
	}

	if cfg.DryRun {
		return res, nil
	}
	if err := r.link(); err != nil {
		return res, err
	}
	if !cfg.NoSetPath {
		path := filepath.Join(cfg.Sys, "module/firmware_class/parameters/path")
		if err := os.WriteFile(path, []byte(res.Target), 0o644); err != nil {
			return res, fmt.Errorf("set firmware path: %w", err)
		}
		res.PathSet = true
	}
	return res, nil
}

// prepareBase makes Base a tmpfs with mnt/ and target/ in it.
//
// The script runs `mount -t tmpfs` on a directory nothing has made, which
// fails ("mount point does not exist") on every boot; its `mkdir -p` then
// makes the directory on whatever file system /run is. So the tmpfs it
// intends never exists. Compat does the same: a tmpfs only if Base is already
// a directory. Otherwise the directory is made first, so the tmpfs is real,
// and a second Run on it is refused.
func (r *runner) prepareBase() error {
	cfg := r.cfg
	if !cfg.Compat && isMountPoint(cfg.Proc, cfg.Base) {
		// The script stacks another tmpfs over the first, hiding its mounts.
		return fmt.Errorf("%s: %w", cfg.Base, ErrLoaded)
	}
	if !cfg.Compat || isDir(cfg.Base) {
		if err := os.MkdirAll(cfg.Base, 0o755); err != nil {
			return err
		}
		if err := cfg.System.MountTmpfs(cfg.Base); err != nil {
			return fmt.Errorf("tmpfs on %s: %w", cfg.Base, err)
		}
		r.res.tmpfs = true
	} else {
		cfg.Logf("mount: mounting none on %s failed: No such file or directory", cfg.Base)
		r.res.plain = true
	}
	for _, d := range []string{"mnt", "target"} {
		if err := os.MkdirAll(filepath.Join(cfg.Base, d), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// mount makes the directory for name (which marks it taken, mounted or not,
// as in the script) and mounts what resolve gives. A failure is recorded.
func (r *runner) mount(name, via, desc string, resolve func() (string, func(), error)) {
	r.claimed[name] = true
	m := Mount{Name: name, Dir: filepath.Join(r.res.Base, "mnt", name), Device: desc, Via: via}
	defer func() {
		if m.Err != nil {
			r.cfg.Logf("mount %s: %v", name, m.Err)
		}
		r.res.Mounts = append(r.res.Mounts, m)
	}()
	if r.cfg.DryRun {
		return
	}
	if m.Err = os.MkdirAll(m.Dir, 0o755); m.Err != nil {
		return
	}
	dev, release, err := resolve()
	if err != nil {
		m.Err = err
		return
	}
	m.Device = dev
	m.Err = r.cfg.System.MountRO(dev, m.Dir)
	release()
	if via == "dm" {
		m.dm = name
		if m.Err != nil {
			r.cfg.System.DMRemove(name)
			m.dm = ""
		}
	}
}

// mountDynamic finds the super partition and mounts the firmware partitions
// inside it. As in the script, the first candidate whose metadata parses is
// the one, and the rest are not looked at.
func (r *runner) mountDynamic(parts []part, isSuper func(string) bool) {
	for _, p := range parts {
		if !isSuper(p.partName) || p.devName == "" {
			continue
		}
		dev := filepath.Join(r.cfg.Dev, p.devName)
		lps, err := r.readSuper(dev)
		if err != nil {
			// "system" on a non-retrofit device, or a stray match.
			r.cfg.Logf("%s (%s): not dynamic partitions: %v", p.partName, p.devName, err)
			continue
		}
		sort.Slice(lps, func(i, j int) bool { return lps[i].Name < lps[j].Name })
		for _, lp := range lps {
			r.mountLogical(dev, lp)
		}
		return
	}
}

// readSuper is the firmware partitions of the super device dev.
//
// make-dynpart-mappings maps only the first metadata slot, so Compat does
// too. Otherwise the slot is the A/B slot's (a is 0, b is 1: the copy Android
// itself maps), and a partition it does not list with extents is looked for
// in the other slots, with a note.
func (r *runner) readSuper(dev string) ([]liblp.Partition, error) {
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pick := func(ps []liblp.Partition) (out []liblp.Partition) {
		for _, p := range ps {
			if r.isFW(p.Name) {
				out = append(out, p)
			}
		}
		return out
	}
	if r.cfg.Compat {
		ps, err := liblp.Partitions(f, 0)
		return pick(ps), err
	}
	n, err := liblp.SlotCount(f)
	if err != nil {
		return nil, err
	}
	want := slotIndex(r.cfg.SlotSuffix)
	order := []int{want}
	for i := 0; i < n; i++ {
		if i != want {
			order = append(order, i)
		}
	}
	have := map[string]bool{}
	var out []liblp.Partition
	var firstErr error
	parsed := false
	for _, slot := range order {
		ps, err := liblp.Partitions(f, slot)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parsed = true
		for _, p := range pick(ps) {
			if have[p.Name] || len(p.Extents) == 0 {
				continue
			}
			have[p.Name] = true
			if slot != want {
				r.cfg.Logf("%s: not in metadata slot %d, using slot %d", p.Name, want, slot)
			}
			out = append(out, p)
		}
	}
	if !parsed {
		return nil, firstErr
	}
	return out, nil
}

// mountLogical mounts a partition of super: through a loop device over its
// extent if it has one, which needs no device-mapper; through dm-linear for
// several, if the kernel has it.
func (r *runner) mountLogical(superDev string, lp liblp.Partition) {
	name := lp.Name
	switch n := len(lp.Extents); {
	case n == 0:
		// Listed for the other slot: the script's dm device is empty and its
		// mount fails, leaving the directory.
		r.mount(name, "loop", "empty", func() (string, func(), error) {
			return "", nil, fmt.Errorf("no extents in this metadata slot")
		})
	case n == 1:
		e := lp.Extents[0]
		desc := fmt.Sprintf("loop over %s sectors %d+%d", superDev, e.Start, e.Sectors)
		r.mount(name, "loop", desc, func() (string, func(), error) {
			return r.cfg.System.LoopOver(superDev, e.Start*512, e.Sectors*512)
		})
	default:
		desc := fmt.Sprintf("dm-linear over %s, %d extents", superDev, n)
		r.mount(name, "dm", desc, func() (string, func(), error) {
			switch {
			case r.cfg.NoDM:
				return "", nil, fmt.Errorf("in %d pieces, and device-mapper is off", n)
			case !r.cfg.System.HasDM():
				return "", nil, fmt.Errorf("in %d pieces, and the kernel has no device-mapper", n)
			}
			dev, err := r.cfg.System.DMCreate(superDev, name, lp.Extents)
			return dev, func() {}, err
		})
	}
}

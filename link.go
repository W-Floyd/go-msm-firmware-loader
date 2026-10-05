package msmfw

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The linking half of the script: from the mounted partitions under
// Base/mnt, build Base/target, the one directory the kernel is pointed at.
// Every step is the script's, in its order; the symlinks are absolute paths
// under Base, as the script's are.

type linker struct {
	cfg    Config
	res    *Result
	base   string
	target string
	mnt    string
	dirs   map[string][]string // directory listings, by path
}

func (r *runner) link() error {
	l := &linker{cfg: r.cfg, res: r.res, base: r.res.Base, target: r.res.Target,
		mnt: filepath.Join(r.res.Base, "mnt"), dirs: map[string][]string{}}
	return l.run()
}

// ln is `ln -s src dst`: a dst that is a directory (symlinks followed) gets
// the link inside it, named like src. Failing is not fatal, as in the script
// (duplicate zap shaders are the usual "File exists").
func (l *linker) ln(src, dst string) {
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	if err := os.Symlink(src, dst); err != nil {
		l.cfg.Logf("ln: %v", err)
		return
	}
	l.res.Links++
}

// names is the sorted, non-hidden entries of dir, nil if it is not readable:
// what a shell glob of dir/* sees.
func (l *linker) names(dir string) []string {
	if n, ok := l.dirs[dir]; ok {
		return n
	}
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	l.dirs[dir] = out
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// hasPrefixEntry is whether dir has an entry (that exists, links followed)
// named with this prefix.
func (l *linker) hasPrefixEntry(dir, prefix string) bool {
	for _, n := range l.names(dir) {
		if strings.HasPrefix(n, prefix) && exists(filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}

func (l *linker) run() error {
	t := func(p ...string) string { return filepath.Join(append([]string{l.target}, p...)...) }

	// The blobs already installed go first, so that they override the
	// partitions'. A dangling link is skipped (the script stops at it).
	if p := l.cfg.ExtraPath; p != "" && isDir(p) {
		for _, n := range l.names(p) {
			if !strings.HasPrefix(n, ".") && exists(filepath.Join(p, n)) {
				l.ln(filepath.Join(p, n), t(n))
			}
		}
	}

	// Every blob of every partition, unless its name (without extension)
	// is already in the target; a blob brings along all its siblings, the
	// files in its directory that start with its name (.mdt and .b00...).
	for _, blob := range l.blobs() {
		dir, name := filepath.Split(blob)
		dir = filepath.Clean(dir)
		base := name
		if i := strings.LastIndex(name, "."); i >= 0 {
			base = name[:i]
		}
		if l.hasPrefixEntry(l.target, base+".") {
			continue
		}
		for _, n := range l.names(dir) {
			if strings.HasPrefix(n, base) && !strings.HasPrefix(n, ".") && isFile(filepath.Join(dir, n)) {
				l.ln(filepath.Join(dir, n), t(n))
			}
		}
		delete(l.dirs, l.target)
	}

	persist := filepath.Join(l.mnt, "persist")
	if isFile(filepath.Join(persist, "sensors/sns.reg")) {
		os.MkdirAll(t("qcom/sensors"), 0o755)
		l.ln(filepath.Join(persist, "sensors/sns.reg"), t("qcom/sensors/sns.reg"))
	}
	if isFile(filepath.Join(persist, "WCNSS_qcom_wlan_nv.bin")) {
		l.ln(filepath.Join(persist, "WCNSS_qcom_wlan_nv.bin"), t("WCNSS_qcom_wlan_nv.bin"))
	}

	// venus blobs are expected in a subdirectory, which differs by platform
	// (as in linux-firmware-qcom), so the one directory is there under
	// every name.
	if isFile(t("venus.mdt")) && !isDir(t("qcom/venus-x")) {
		os.MkdirAll(t("qcom/venus-x"), 0o755)
		delete(l.dirs, l.target)
		for _, n := range l.names(l.target) {
			if strings.HasPrefix(n, "venus.") {
				l.ln(t(n), t("qcom/venus-x", n))
			}
		}
	}
	for _, v := range []string{"venus-1.8", "venus-3.0", "venus-4.2", "venus-4.4",
		"venus-5.2", "venus-5.4", "vpu-1.0", "vpu-2.0"} {
		if !isDir(t("qcom", v)) && isFile(t("venus.mdt")) {
			l.ln(t("qcom/venus-x"), t("qcom", v))
		}
	}

	// The wlan nv file is expected in a subdirectory too.
	if isLink(t("WCNSS_qcom_wlan_nv.bin")) && !isFile(t("wlan/prima/WCNSS_qcom_wlan_nv.bin")) {
		os.MkdirAll(t("wlan/prima"), 0o755)
		l.ln(t("WCNSS_qcom_wlan_nv.bin"), t("wlan/prima"))
	}

	// ath10k wcn3990 devices keep Bluetooth firmware on a partition of its
	// own; the kernel wants it under qca/.
	if bt := filepath.Join(l.mnt, "bluetooth"+l.cfg.SlotSuffix); isDir(bt) {
		os.MkdirAll(t("qca"), 0o755)
		for _, n := range l.names(filepath.Join(bt, "image")) {
			if !strings.HasPrefix(n, ".") {
				l.ln(filepath.Join(bt, "image", n), t("qca"))
			}
		}
	}

	// The modem fetches its RF configuration over tqftpserv from fsg/ next to
	// the remoteproc firmware (the downstream patch to the script).
	if fsg := filepath.Join(l.mnt, "fsg"+l.cfg.SlotSuffix); isDir(fsg) && !exists(t("fsg")) {
		l.ln(fsg, t("fsg"))
	}

	// Some kernels want .mbn where the partitions have .mdt: link them, the
	// kernel can tell which it got. This is find(1): symlinks are listed but
	// not entered, and the new links are no .mdt.
	l.mdtToMbn(l.target)

	// A devicetree names its firmware with a per-device prefix, so
	// similarly named blobs for different devices are told apart. Assume
	// all of a device's blobs share it and link the tree there.
	if prefix := firmwarePrefix(l.cfg.Sys); prefix != "" {
		os.MkdirAll(t(filepath.Dir(prefix)), 0o755)
		l.ln(l.target, t(prefix))
	}
	return nil
}

// blobs is the script's `mnt/*/image/* mnt/*/firmware/*`, in glob order, the
// ones that exist.
func (l *linker) blobs() []string {
	var out []string
	for _, sub := range []string{"image", "firmware"} {
		var group []string
		for _, p := range l.names(l.mnt) {
			if strings.HasPrefix(p, ".") {
				continue
			}
			for _, n := range l.names(filepath.Join(l.mnt, p, sub)) {
				if !strings.HasPrefix(n, ".") {
					group = append(group, filepath.Join(l.mnt, p, sub, n))
				}
			}
		}
		sort.Strings(group)
		for _, b := range group {
			if exists(b) {
				out = append(out, b)
			}
		}
	}
	return out
}

func (l *linker) mdtToMbn(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var sub []string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if strings.HasSuffix(e.Name(), ".mdt") {
			l.ln(p, strings.TrimSuffix(p, ".mdt")+".mbn")
		}
		if e.IsDir() {
			sub = append(sub, p)
		}
	}
	for _, p := range sub {
		l.mdtToMbn(p)
	}
}

// firmwarePrefix is the directory part of the first firmware-name property
// in the devicetree, "" if there is none.
func firmwarePrefix(sysRoot string) string {
	name := findFirst(filepath.Join(sysRoot, "firmware/devicetree"), "firmware-name")
	if name == "" {
		return ""
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	// A property is a NUL-terminated string; xargs takes the first word.
	f := strings.Fields(strings.ReplaceAll(string(b), "\x00", " "))
	if len(f) == 0 {
		return ""
	}
	return filepath.Dir(f[0])
}

// findFirst is the first file called name below dir, in directory order as
// find(1) walks it (readdir order, which os.ReadDir would sort).
func findFirst(dir, name string) string {
	f, err := os.Open(dir)
	if err != nil {
		return ""
	}
	ents, _ := f.ReadDir(-1)
	f.Close()
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.Name() == name {
			return p
		}
		if e.IsDir() {
			if r := findFirst(p, name); r != "" {
				return r
			}
		}
	}
	return ""
}

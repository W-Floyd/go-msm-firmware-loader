package msmfw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// genericWorld is a fake head unit: its stock system partition, a persist
// partition with a second copy of one file, and a boot partition that does
// not mount (no fixture).
func genericWorld(t *testing.T) (Config, *fakeSys) {
	t.Helper()
	root := t.TempDir()
	sysd, dev := filepath.Join(root, "sys"), filepath.Join(root, "dev")
	os.MkdirAll(dev, 0o755)
	for _, p := range [][3]string{{"mmcblk0", "mmcblk0p1", "system"}, {"mmcblk0", "mmcblk0p2", "persist"}, {"mmcblk0", "mmcblk0p3", "boot"}} {
		d := filepath.Join(sysd, "block", p[0], p[1])
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "uevent"), []byte("DEVNAME="+p[1]+"\nPARTNAME="+p[2]+"\n"), 0o644)
		os.WriteFile(filepath.Join(dev, p[1]), nil, 0o644)
	}
	os.MkdirAll(filepath.Join(sysd, "module/firmware_class/parameters"), 0o755)
	fake := &fakeSys{fixtures: map[string]map[string]string{
		"mmcblk0p1": {
			"vendor/firmware/bcm4349.hcd":          "L2101",
			"vendor/firmware/bcm4349.hcd_E0300":    "E0300",
			"vendor/firmware/bcm20702.hcd":         "b20702",
			"wifi/fw_bcmdhd.bin":                   "WFW",
			"wifi/bcmdhd.cal":                      "CAL",
			"deep/a/b/c/d/e/f/g/too_deep.bin":      "deep",
			"vendor/firmware/notes/readme.txt":     "x",
			"vendor/firmware/bcm4349.hcd_L2101":    "L2101",
			"vendor/firmware/other/bcm4349.hcd":    "shadowed, later in name order",
			"vendor/firmware/other/89359.clm_blob": "from system (first partition wins)",
		},
		"mmcblk0p2": {"x/fw_bcmdhd.bin": "second copy", "y/89359.clm_blob": "CLM"},
	}}
	return Config{
		Base: filepath.Join(root, "run"), Sys: sysd, Dev: dev, Proc: filepath.Join(root, "proc"),
		NoSetPath: true, System: fake, Logf: func(f string, a ...any) { t.Logf(f, a...) },
	}, fake
}

func mustRules(t *testing.T, specs ...string) []Rule {
	t.Helper()
	var out []Rule
	for _, s := range specs {
		r, err := ParseRule(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGenericLinksFirstPartitionWins(t *testing.T) {
	cfg, _ := genericWorld(t)
	cfg.Rules = mustRules(t,
		"fw_bcmdhd.bin=brcm/brcmfmac4359-sdio.bin",
		"bcmdhd.cal=brcm/brcmfmac4359-sdio.txt",
		"89359.clm_blob=brcm/brcmfmac4359-sdio.clm_blob",
		"bcm4349.hcd=brcm/BCM.hcd",
		"bcm4349.hcd=brcm/BCM4349B1.hcd", // an alias: one file, two names
		"missing.bin=x/missing.bin",
	)
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// persist has a second fw_bcmdhd.bin; system is first in the disk's order.
	if got := read(t, filepath.Join(res.Target, "brcm/brcmfmac4359-sdio.bin")); got != "WFW" {
		t.Errorf("fw_bcmdhd.bin = %q, want the first partition's", got)
	}
	// Within system, name order: vendor/firmware/bcm4349.hcd before .../other/.
	for _, n := range []string{"brcm/BCM.hcd", "brcm/BCM4349B1.hcd"} {
		if got := read(t, filepath.Join(res.Target, n)); got != "L2101" {
			t.Errorf("%s = %q", n, got)
		}
	}
	// The clm blob is in both: system's wins.
	if got := read(t, filepath.Join(res.Target, "brcm/brcmfmac4359-sdio.clm_blob")); !strings.HasPrefix(got, "from system") {
		t.Errorf("clm_blob = %q", got)
	}
	// They are links into the mounted partition, which stays mounted.
	dest, err := os.Readlink(filepath.Join(res.Target, "brcm/BCM.hcd"))
	if err != nil || !strings.HasSuffix(dest, "mnt/system/vendor/firmware/bcm4349.hcd") {
		t.Errorf("link = %q, %v", dest, err)
	}
	if len(res.Missing) != 1 || res.Missing[0].Find != "missing.bin" {
		t.Errorf("Missing = %v", res.Missing)
	}
	if res.Links != 5 {
		t.Errorf("Links = %d, want 5", res.Links)
	}
	// boot did not mount, and that is recorded, not fatal.
	failed := 0
	for _, m := range res.Mounts {
		if m.Err != nil {
			failed++
			if m.Name != "boot" {
				t.Errorf("%s failed to mount", m.Name)
			}
		}
	}
	if failed != 1 || len(res.Mounts) != 3 {
		t.Errorf("mounts = %+v", res.Mounts)
	}
}

func TestGenericCopyReleasesPartitions(t *testing.T) {
	cfg, fake := genericWorld(t)
	cfg.Copy = true
	cfg.Rules = mustRules(t, "fw_bcmdhd.bin=brcm/a.bin", "bcm4349.hcd=brcm/BCM.hcd")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{"brcm/a.bin": "WFW", "brcm/BCM.hcd": "L2101"} {
		p := filepath.Join(res.Target, n)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s: %v %v", n, fi, err)
		}
		if read(t, p) != want {
			t.Errorf("%s = %q", n, read(t, p))
		}
	}
	if res.Copied != 2 || res.Links != 0 {
		t.Errorf("Copied %d Links %d", res.Copied, res.Links)
	}
	// The stock partitions were let go: unmounted once each, and not again by Close.
	count := func(name string) (n int) {
		for _, u := range fake.unmounted {
			if strings.HasSuffix(u, "mnt/"+name) {
				n++
			}
		}
		return
	}
	if count("system") != 1 || count("persist") != 1 || count("boot") != 0 {
		t.Errorf("unmounted = %v", fake.unmounted)
	}
	if err := res.Close(); err != nil {
		t.Error(err)
	}
	if count("system") != 1 {
		t.Errorf("Close unmounted system again: %v", fake.unmounted)
	}
}

func TestGenericDirectoryRule(t *testing.T) {
	cfg, _ := genericWorld(t)
	cfg.Rules = mustRules(t, "bcm*.hcd=brcm/")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(res.Target, "brcm"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	// bcm4349.hcd_E0300 does not end in .hcd; the shadowed copy shares a name.
	if strings.Join(names, " ") != "bcm20702.hcd bcm4349.hcd" {
		t.Errorf("brcm/ = %v", names)
	}
	if got := read(t, filepath.Join(res.Target, "brcm/bcm4349.hcd")); got != "L2101" {
		t.Errorf("bcm4349.hcd = %q", got)
	}
}

func TestGenericMaxDepth(t *testing.T) {
	cfg, _ := genericWorld(t)
	cfg.Rules = mustRules(t, "too_deep.bin=deep.bin")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 1 {
		t.Errorf("found a file 8 directories down at the default depth: %v", res.Missing)
	}
	cfg2, _ := genericWorld(t)
	cfg2.MaxDepth = 8
	cfg2.Rules = mustRules(t, "too_deep.bin=deep.bin")
	res2, err := Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Missing) != 0 || read(t, filepath.Join(res2.Target, "deep.bin")) != "deep" {
		t.Errorf("MaxDepth 8: missing %v", res2.Missing)
	}
}

func TestGenericPartitionsList(t *testing.T) {
	cfg, fake := genericWorld(t)
	cfg.Partitions = []string{"persist"}
	cfg.Rules = mustRules(t, "fw_bcmdhd.bin=a.bin")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(res.Target, "a.bin")); got != "second copy" {
		t.Errorf("a.bin = %q: system should not have been looked at", got)
	}
	if len(res.Mounts) != 1 || res.Mounts[0].Name != "persist" {
		t.Errorf("mounts = %+v", res.Mounts)
	}
	_ = fake
}

func TestGenericExtraPathAndKept(t *testing.T) {
	cfg, _ := genericWorld(t)
	extra := t.TempDir()
	os.WriteFile(filepath.Join(extra, "keep.bin"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(extra, "a.bin"), []byte("extra wins"), 0o644)
	cfg.ExtraPath = extra
	cfg.Rules = mustRules(t, "fw_bcmdhd.bin=a.bin")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(res.Target, "keep.bin")); got != "mine" {
		t.Errorf("keep.bin = %q", got)
	}
	// The extra path's own a.bin is already there: the rule keeps it.
	if got := read(t, filepath.Join(res.Target, "a.bin")); got != "extra wins" {
		t.Errorf("a.bin = %q", got)
	}
	if len(res.Missing) != 0 {
		t.Errorf("a kept file was reported missing: %v", res.Missing)
	}
}

func TestGenericDryRunAndCompat(t *testing.T) {
	cfg, _ := genericWorld(t)
	cfg.DryRun = true
	cfg.Rules = mustRules(t, "fw_bcmdhd.bin=a.bin")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mounts) != 3 || res.Links != 0 {
		t.Errorf("dry run: mounts %d links %d", len(res.Mounts), res.Links)
	}
	if _, err := os.Stat(res.Target); err == nil {
		t.Error("a dry run made the target")
	}
	cfg2, _ := genericWorld(t)
	cfg2.Compat = true
	cfg2.Rules = mustRules(t, "x=y")
	if _, err := Run(cfg2); err == nil {
		t.Error("rules with Compat were accepted")
	}
}

func TestParseRule(t *testing.T) {
	for in, want := range map[string]Rule{
		"fw.bin=brcm/fw.bin":   {"fw.bin", "brcm/fw.bin"},
		" a = b//c/ ":          {"a", "b/c/"},
		"bcm*.hcd=brcm/":       {"bcm*.hcd", "brcm/"},
		"x=./y/../z":           {"x", "z"},
		"89359.clm_blob=a/b.c": {"89359.clm_blob", "a/b.c"},
	} {
		got, err := ParseRule(in)
		if err != nil || got != want {
			t.Errorf("ParseRule(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"noequals", "=x", "x=", "a/b=c", "x=../y", "x=/abs", "x=..", "[=y"} {
		if r, err := ParseRule(in); err == nil {
			t.Errorf("ParseRule(%q) = %v, want an error", in, r)
		}
	}
	if r := (Rule{Find: "a=b"}); r.String() != "a=b=" {
		t.Errorf("String = %q", r.String())
	}
}

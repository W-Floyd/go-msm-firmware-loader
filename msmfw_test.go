package msmfw

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

// fakeSys stands in for the kernel: a "mount" fills the directory from a
// fixture keyed by the device it was asked to mount.
type fakeSys struct {
	fixtures  map[string]map[string]string
	unmounted []string
	loops     []string
	dm        bool
}

func (f *fakeSys) MountTmpfs(dir string) error { return nil }
func (f *fakeSys) Unmount(dir string) error    { f.unmounted = append(f.unmounted, dir); return nil }
func (f *fakeSys) HasDM() bool                 { return f.dm }
func (f *fakeSys) DMRemove(string) error       { return nil }
func (f *fakeSys) DMCreate(_, name string, _ []Extent) (string, error) {
	return "dm:" + name, nil
}
func (f *fakeSys) LoopOver(backing string, off, size uint64) (string, func(), error) {
	dev := "loop:" + itoa(off/512)
	f.loops = append(f.loops, dev)
	return dev, func() {}, nil
}
func (f *fakeSys) MountRO(dev, dir string) error {
	// Devices are told by their last path element, or loop:<start sector>.
	files, ok := f.fixtures[filepath.Base(dev)]
	if !ok {
		files, ok = f.fixtures[dev]
	}
	if !ok {
		return errors.New("wrong fs type")
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	return nil
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// world builds a fake sysfs and /dev: a super partition (the
// synthetic header), the firmware partitions of a device, and a partition "sp" that
// does not mount.
func world(t *testing.T) (Config, *fakeSys) {
	t.Helper()
	root := t.TempDir()
	sysd, dev := filepath.Join(root, "sys"), filepath.Join(root, "dev")
	os.MkdirAll(dev, 0o755)
	for disk, parts := range map[string][][2]string{
		"sda": {{"sda1", "modem_a"}, {"sda2", "super"}, {"sda3", "boot_a"}, {"sda4", "persist"}},
		"sdb": {{"sdb1", "dsp_a"}, {"sdb2", "sp"}, {"sdb3", "bluetooth_a"}, {"sdb4", "fsg_a"}},
	} {
		for _, p := range parts {
			d := filepath.Join(sysd, "block", disk, p[0])
			os.MkdirAll(d, 0o755)
			os.WriteFile(filepath.Join(d, "uevent"), []byte("MAJOR=8\nMINOR=1\nDEVNAME="+p[0]+"\nPARTNAME="+p[1]+"\n"), 0o644)
			os.WriteFile(filepath.Join(dev, p[0]), nil, 0o644)
		}
	}
	super := liblp.FixtureSuper()
	os.WriteFile(filepath.Join(dev, "sda2"), super, 0o644)

	fw := filepath.Join(sysd, "firmware/devicetree/base/soc/remoteproc")
	os.MkdirAll(fw, 0o755)
	os.WriteFile(filepath.Join(fw, "firmware-name"), []byte("qcom/sm6225/moto/adsp.mbn\x00"), 0o644)
	os.MkdirAll(filepath.Join(sysd, "module/firmware_class/parameters"), 0o755)

	fake := &fakeSys{fixtures: map[string]map[string]string{
		"sda1": {"image/modem.mdt": "m", "image/modem.b00": "b0", "image/wlanmdsp.mbn": "w1", "image/modem_pr/x": "dir sibling"},
		"sda4": {"sensors/sns.reg": "reg", "WCNSS_qcom_wlan_nv.bin": "nv"},
		"sdb1": {"image/adsp.mdt": "a", "image/adsp.b00": "a0"},
		"sdb3": {"image/crnv32.bin": "n", "image/crbtfw32.tlv": "t"},
		"sdb4": {"mcfg/x": "mcfg"},
		// vendor_a is the fixture super's: a single extent.
		"loop:" + itoa(liblp.FixtureVendorA.Start): {"firmware/venus.mdt": "v", "firmware/venus.b00": "v0",
			"firmware/a660_zap.mdt": "z", "firmware/a660_zap.b00": "z0",
			"firmware/wlanmdsp.mbn": "w2", "firmware/notes.txt": "n"},
	}}
	return Config{
		Base: filepath.Join(root, "run"), SlotSuffix: "_a", Sys: sysd, Dev: dev,
		Proc: filepath.Join(root, "proc"), NoSetPath: true, System: fake,
		Logf: func(f string, a ...any) { t.Logf(f, a...) },
	}, fake
}

func link(t *testing.T, res *Result, rel, wantDest string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(res.Target, rel))
	if err != nil {
		t.Errorf("%s: %v", rel, err)
		return
	}
	want := strings.ReplaceAll(wantDest, "@", res.Base)
	if got != want {
		t.Errorf("%s -> %s; want %s", rel, got, want)
	}
}

func TestRunLinks(t *testing.T) {
	cfg, fake := world(t)
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mounted := map[string]Mount{}
	for _, m := range res.Mounts {
		mounted[m.Name] = m
	}
	for _, n := range []string{"modem_a", "dsp_a", "bluetooth_a", "fsg_a", "persist", "vendor_a"} {
		if m, ok := mounted[n]; !ok || m.Err != nil {
			t.Errorf("%s: %+v", n, m)
		}
	}
	if mounted["vendor_a"].Via != "loop" {
		t.Errorf("vendor_a via %q", mounted["vendor_a"].Via)
	}
	// "sp" is mounted because "dsp_a" contains it, and fails; the directory stays.
	if m, ok := mounted["sp"]; !ok || m.Err == nil {
		t.Errorf("sp: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(res.Base, "mnt/sp")); err != nil {
		t.Error(err)
	}
	if _, ok := mounted["boot_a"]; ok {
		t.Error("boot_a mounted")
	}

	link(t, res, "modem.mdt", "@/mnt/modem_a/image/modem.mdt")
	link(t, res, "modem.b00", "@/mnt/modem_a/image/modem.b00")
	link(t, res, "modem.mbn", "@/target/modem.mdt")
	link(t, res, "adsp.b00", "@/mnt/dsp_a/image/adsp.b00")
	// The first partition with a name wins (modem_a sorts before vendor_a).
	link(t, res, "wlanmdsp.mbn", "@/mnt/modem_a/image/wlanmdsp.mbn")
	link(t, res, "a660_zap.b00", "@/mnt/vendor_a/firmware/a660_zap.b00")
	// A sibling is anything that starts with the blob's name.
	if _, err := os.Lstat(filepath.Join(res.Target, "modem_pr")); err == nil {
		t.Error("a directory was linked as a sibling")
	}
	// Every file of firmware/ is a blob, whatever its name.
	if _, err := os.Lstat(filepath.Join(res.Target, "notes.txt")); err != nil {
		t.Error("notes.txt is in firmware/ and should be linked")
	}
	link(t, res, "qcom/sensors/sns.reg", "@/mnt/persist/sensors/sns.reg")
	link(t, res, "WCNSS_qcom_wlan_nv.bin", "@/mnt/persist/WCNSS_qcom_wlan_nv.bin")
	link(t, res, "wlan/prima/WCNSS_qcom_wlan_nv.bin", "@/target/WCNSS_qcom_wlan_nv.bin")
	link(t, res, "qcom/venus-x/venus.b00", "@/target/venus.b00")
	// find(1) enters the real venus-x directory, so it gets its .mbn too.
	link(t, res, "qcom/venus-x/venus.mbn", "@/target/qcom/venus-x/venus.mdt")
	link(t, res, "qcom/venus-5.4", "@/target/qcom/venus-x")
	link(t, res, "qca/crnv32.bin", "@/mnt/bluetooth_a/image/crnv32.bin")
	link(t, res, "fsg", "@/mnt/fsg_a")
	link(t, res, "qcom/sm6225/moto", "@/target")

	// Closing unmounts the partitions, last first, and the tmpfs.
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.unmounted); n == 0 || fake.unmounted[n-1] != res.Base {
		t.Errorf("unmounted %v", fake.unmounted)
	}
	if err := res.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestRunExactMatch(t *testing.T) {
	cfg, _ := world(t)
	cfg.ExactMatch = true
	cfg.DryRun = true
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Mounts {
		if m.Name == "sp" {
			t.Error("sp matched exactly")
		}
	}
	if len(res.Mounts) != 6 {
		t.Errorf("%d mounts: %+v", len(res.Mounts), res.Mounts)
	}
	if _, err := os.Stat(res.Base); err == nil {
		t.Error("a dry run made the base")
	}
}

func TestRunExtraPathWinsAndNoDM(t *testing.T) {
	cfg, _ := world(t)
	extra := t.TempDir()
	os.WriteFile(filepath.Join(extra, "modem.mdt"), []byte("mine"), 0o644)
	cfg.ExtraPath = extra
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	link(t, res, "modem.mdt", extra+"/modem.mdt")
	// modem.mdt is taken, so modem_a's modem.* are all skipped.
	if _, err := os.Lstat(filepath.Join(res.Target, "modem.b00")); err == nil {
		t.Error("modem.b00 linked past the preinstalled modem.mdt")
	}
}

func TestSlotSuffixFromCmdline(t *testing.T) {
	for in, want := range map[string]string{
		"console=ttyMSM0 androidboot.slot_suffix=_b quiet": "_b",
		"quiet": "",
	} {
		if got := SlotSuffixFromCmdline(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestMatcher(t *testing.T) {
	list := DefaultPartitions("_a")
	sub, exact := matcher(list, false), matcher(list, true)
	for name, want := range map[string][2]bool{
		"sp": {true, false}, "dsp_a": {true, true}, "dsp_b": {false, false},
		"persist": {true, true}, "": {false, false}, "boot_a": {false, false},
	} {
		if sub(name) != want[0] || exact(name) != want[1] {
			t.Errorf("%q: substring %v exact %v; want %v", name, sub(name), exact(name), want)
		}
	}
}

func TestSecondRunRefused(t *testing.T) {
	cfg, _ := world(t)
	os.MkdirAll(filepath.Join(cfg.Proc, "self"), 0o755)
	os.WriteFile(filepath.Join(cfg.Proc, "self/mountinfo"), []byte("1 0 0:1 / "+cfg.Base+" rw - tmpfs none rw\n"), 0o644)
	if _, err := Run(cfg); !errors.Is(err, ErrLoaded) {
		t.Errorf("err = %v", err)
	}
}

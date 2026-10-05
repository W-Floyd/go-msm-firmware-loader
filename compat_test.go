package msmfw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

// Each test here is one of the script's bugs, run both ways: the default
// fixes it, Compat keeps it. The README has the table.

// runLogged runs cfg and returns what it logged.
func runLogged(t *testing.T, cfg Config) (*Result, []string) {
	t.Helper()
	var logs []string
	cfg.Logf = func(f string, a ...any) { logs = append(logs, strings.TrimSpace(sprintf(f, a...))) }
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Close() })
	return res, logs
}

func sprintf(f string, a ...any) string { return strings.TrimSpace(fmt.Sprintf(f, a...)) }

func anyContains(logs []string, sub string) bool {
	return slices.ContainsFunc(logs, func(s string) bool { return strings.Contains(s, sub) })
}

func lexists(res *Result, rel string) bool {
	_, err := os.Lstat(filepath.Join(res.Target, rel))
	return err == nil
}

// 1. No slot suffix on an A/B device: the default says so.
func TestNoSlotSuffixWarning(t *testing.T) {
	cfg, _ := world(t)
	cfg.SlotSuffix = ""
	cfg.DryRun = true
	_, logs := runLogged(t, cfg)
	if !anyContains(logs, "no slot suffix") {
		t.Errorf("no warning: %v", logs)
	}
	cfg.Compat = true
	_, logs = runLogged(t, cfg)
	if anyContains(logs, "no slot suffix") {
		t.Errorf("compat warned: %v", logs)
	}
	cfg.Compat, cfg.SlotSuffix = false, "_a"
	_, logs = runLogged(t, cfg)
	if anyContains(logs, "no slot suffix") {
		t.Errorf("warned with a suffix: %v", logs)
	}
}

// 2. The tmpfs.
func TestTmpfs(t *testing.T) {
	cfg, fake := world(t)
	res, _ := runLogged(t, cfg)
	if len(fake.tmpfs) != 1 || fake.tmpfs[0] != res.Base {
		t.Errorf("default: tmpfs mounts %v", fake.tmpfs)
	}

	cfg, fake = world(t)
	cfg.Compat = true
	res, logs := runLogged(t, cfg)
	if len(fake.tmpfs) != 0 || !anyContains(logs, "No such file or directory") {
		t.Errorf("compat, no base: tmpfs %v, logs %v", fake.tmpfs, logs)
	}
	if !lexists(res, "modem.mdt") {
		t.Error("compat carried on without a tmpfs but linked nothing")
	}
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Base); err == nil {
		t.Error("Close left the plain directory")
	}

	cfg, fake = world(t)
	cfg.Compat = true
	os.MkdirAll(cfg.Base, 0o755)
	runLogged(t, cfg)
	if len(fake.tmpfs) != 1 {
		t.Errorf("compat, base exists: tmpfs %v", fake.tmpfs)
	}
}

// 3. Substring matching is in TestRunMatching.

// 4. A dangling link in the preselected path.
func TestDanglingExtraPath(t *testing.T) {
	for _, compat := range []bool{false, true} {
		cfg, _ := world(t)
		cfg.Compat = compat
		extra := t.TempDir()
		os.Symlink("/nonexistent", filepath.Join(extra, "a-dangling"))
		os.WriteFile(filepath.Join(extra, "z-later.bin"), []byte("x"), 0o644)
		cfg.ExtraPath = extra
		res, _ := runLogged(t, cfg)
		if got := lexists(res, "z-later.bin"); got == compat {
			t.Errorf("compat=%v: blob after the dangling link linked: %v", compat, got)
		}
		if lexists(res, "a-dangling") {
			t.Error("the dangling link was linked")
		}
	}
}

// 5. An empty bluetooth image/ and the bare glob.
func TestEmptyBluetoothGlob(t *testing.T) {
	for _, compat := range []bool{false, true} {
		cfg, fake := world(t)
		cfg.Compat = compat
		fake.fixtures["sdb3"] = map[string]string{"nothing-here": "x"}
		res, _ := runLogged(t, cfg)
		star := lexists(res, "qca/*")
		if star != compat {
			t.Errorf("compat=%v: qca/* exists: %v", compat, star)
		}
		if compat {
			link(t, res, "qca/*", "@/mnt/bluetooth_a/image/*")
		} else if lexists(res, "qca") {
			t.Error("qca made for nothing")
		}
	}
}

// 5b. A bluetooth or fsg partition that did not mount is no partition,
// though its directory is there.
func TestFailedMountIsNotLinked(t *testing.T) {
	for _, compat := range []bool{false, true} {
		cfg, fake := world(t)
		cfg.Compat = compat
		delete(fake.fixtures, "sdb4") // fsg_a fails to mount
		res, _ := runLogged(t, cfg)
		if got := lexists(res, "fsg"); got != compat {
			t.Errorf("compat=%v: fsg link: %v", compat, got)
		}
	}
}

// 6. .mbn for .mdt, and "File exists".
func TestMbnFileExists(t *testing.T) {
	for _, compat := range []bool{false, true} {
		cfg, fake := world(t)
		cfg.Compat = compat
		fake.fixtures["sda1"]["image/foo.mdt"] = "m"
		fake.fixtures["sda1"]["image/foo.mbn"] = "b"
		res, logs := runLogged(t, cfg)
		exist := anyContains(logs, "file exists")
		if exist != compat {
			t.Errorf("compat=%v: 'file exists' logged: %v (%v)", compat, exist, logs)
		}
		link(t, res, "foo.mbn", "@/mnt/modem_a/image/foo.mbn") // the partition's own wins
		link(t, res, "modem.mbn", "@/target/modem.mdt")        // a missing one is still made
	}
}

// 6b. Making the link that is already there is no error.
func TestLnIdentical(t *testing.T) {
	for _, compat := range []bool{false, true} {
		dir := t.TempDir()
		var logs []string
		l := &linker{cfg: Config{Compat: compat, Logf: func(f string, a ...any) { logs = append(logs, f) }}, res: &Result{}}
		l.ln("/x", filepath.Join(dir, "a"))
		l.ln("/x", filepath.Join(dir, "a"))
		l.ln("/y", filepath.Join(dir, "a")) // a different link is a real conflict
		want := 1
		if compat {
			want = 2
		}
		if len(logs) != want {
			t.Errorf("compat=%v: %d errors logged: %v", compat, len(logs), logs)
		}
	}
}

// 7. The metadata slot.
func TestMetadataSlotFallback(t *testing.T) {
	for _, compat := range []bool{false, true} {
		cfg, fake := world(t)
		cfg.Compat = compat
		// vendor_a is only in slot 1's metadata, and slot 0 lists it empty.
		img := liblp.Build([][]liblp.Partition{
			{{Name: "vendor_a"}},
			{{Name: "vendor_a", Extents: []liblp.Extent{{Start: 777, Sectors: 10}}}},
		})
		os.WriteFile(filepath.Join(cfg.Dev, "sda2"), img, 0o644)
		fake.fixtures["loop:777"] = map[string]string{"firmware/gpu.bin": "g"}
		res, logs := runLogged(t, cfg)
		var vendor Mount
		for _, m := range res.Mounts {
			if m.Name == "vendor_a" {
				vendor = m
			}
		}
		if compat {
			if vendor.Err == nil || lexists(res, "gpu.bin") {
				t.Errorf("compat used a slot other than the first: %+v", vendor)
			}
			continue
		}
		if vendor.Err != nil || !lexists(res, "gpu.bin") {
			t.Errorf("fallback: %+v", vendor)
		}
		if !anyContains(logs, "vendor_a: not in metadata slot 0, using slot 1") {
			t.Errorf("not said: %v", logs)
		}
	}
}

// 7b. _b is metadata slot 1.
func TestMetadataSlotFollowsSuffix(t *testing.T) {
	cfg, fake := world(t)
	cfg.SlotSuffix = "_b"
	img := liblp.Build([][]liblp.Partition{
		{{Name: "vendor_b", Extents: []liblp.Extent{{Start: 1, Sectors: 1}}}},
		{{Name: "vendor_b", Extents: []liblp.Extent{{Start: 888, Sectors: 1}}}},
	})
	os.WriteFile(filepath.Join(cfg.Dev, "sda2"), img, 0o644)
	fake.fixtures["loop:888"] = map[string]string{"firmware/b.bin": "b"}
	res, logs := runLogged(t, cfg)
	if !lexists(res, "b.bin") || anyContains(logs, "using slot") {
		t.Errorf("logs %v", logs)
	}
}

// 8. Twice.
func TestRunTwice(t *testing.T) {
	cfg, _ := world(t)
	res, _ := runLogged(t, cfg)
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	res2, _ := runLogged(t, cfg) // after Close, fine
	_ = res2

	// While loaded (mountinfo lists Base): refused, and nothing to Close.
	os.MkdirAll(filepath.Join(cfg.Proc, "self"), 0o755)
	os.WriteFile(filepath.Join(cfg.Proc, "self/mountinfo"), []byte("1 0 0:1 / "+cfg.Base+" rw - tmpfs none rw\n"), 0o644)
	if res, err := Run(cfg); !errors.Is(err, ErrLoaded) || res != nil {
		t.Errorf("default: %v, %v", res, err)
	}
	cfg.Compat = true
	res3, err := Run(cfg) // the script stacks another tmpfs
	if err != nil {
		t.Errorf("compat: %v", err)
	}
	res3.Close()
}

// 9. Several firmware-name properties.
func TestFirmwarePrefixes(t *testing.T) {
	sysd := t.TempDir()
	write := func(dir, content string) {
		d := filepath.Join(sysd, "firmware/devicetree/base", dir)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "firmware-name"), []byte(content), 0o644)
	}
	write("remoteproc@1", "qcom/sm6225/moto/adsp.mbn\x00")
	write("remoteproc@2", "qcom/sm6225/other/cdsp.mbn\x00")
	write("wifi", "qcom/sm6225/moto/wlanmdsp.mbn\x00") // the same prefix again
	write("modem", "qcom/sm6225/moto/mba.mbn\x00qcom/sm6225/modem/modem.mbn\x00")
	write("venus", "venus.mbn\x00") // no prefix
	got := firmwarePrefixes(sysd, false)
	want := []string{"qcom/sm6225/modem", "qcom/sm6225/moto", "qcom/sm6225/other"}
	if !slices.Equal(got, want) {
		t.Errorf("default: %v; want %v", got, want)
	}
	if got := firmwarePrefixes(sysd, true); len(got) != 1 || !strings.HasPrefix(got[0], "qcom/sm6225/") && got[0] != "." {
		t.Errorf("compat: %v", got)
	}
}

func TestFirmwarePrefixLinks(t *testing.T) {
	cfg, _ := world(t)
	d := filepath.Join(cfg.Sys, "firmware/devicetree/base/soc/other")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "firmware-name"), []byte("qcom/sm6225/other/cdsp.mbn\x00"), 0o644)
	res, _ := runLogged(t, cfg)
	link(t, res, "qcom/sm6225/moto", "@/target")
	link(t, res, "qcom/sm6225/other", "@/target")

	cfg, _ = world(t)
	cfg.Compat = true
	os.MkdirAll(d, 0o755)
	d = filepath.Join(cfg.Sys, "firmware/devicetree/base/soc/other")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "firmware-name"), []byte("qcom/sm6225/other/cdsp.mbn\x00"), 0o644)
	res, _ = runLogged(t, cfg)
	n := 0
	for _, p := range []string{"qcom/sm6225/moto", "qcom/sm6225/other"} {
		if lexists(res, p) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("compat linked %d prefixes", n)
	}
}

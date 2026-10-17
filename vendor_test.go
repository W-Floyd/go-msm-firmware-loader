package msmfw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltInVendors(t *testing.T) {
	got := strings.Join(Vendors(), ",")
	if !strings.Contains(got, "generic") || !strings.Contains(got, "qcom") {
		t.Errorf("Vendors() = %s", got)
	}
	if v, ok := LookupVendor("qcom"); !ok || v.Name() != "qcom" {
		t.Error("no qcom vendor")
	}
}

func TestVendorSelectionAndValidation(t *testing.T) {
	cfg, _ := genericWorld(t)
	cfg.Vendor = "nope"
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), `no vendor "nope"`) {
		t.Errorf("unknown vendor: %v", err)
	}

	// Rules without a vendor mean generic, but qcom named with rules does not fit.
	cfg, _ = genericWorld(t)
	cfg.Vendor = "qcom"
	cfg.Rules = mustRules(t, "x=y")
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "generic vendor's") {
		t.Errorf("qcom with rules: %v", err)
	}

	// Generic without rules has nothing to do.
	cfg, _ = genericWorld(t)
	cfg.Vendor = "generic"
	if _, err := Run(cfg); err == nil || !strings.Contains(err.Error(), "no rules") {
		t.Errorf("generic without rules: %v", err)
	}

	// Copy is for vendors that place files through Env.Place.
	cfg, _ = genericWorld(t)
	cfg.Vendor = "qcom"
	cfg.Copy = true
	if _, err := Run(cfg); err == nil {
		t.Error("qcom with Copy was accepted")
	}
}

// fixedVendor takes firmware from one partition and puts what it finds there
// under a driver's name: a vendor written outside the built-ins.
type fixedVendor struct {
	partitions []string
	linked     []string
}

func (*fixedVendor) Name() string                 { return "fixed" }
func (*fixedVendor) Validate(Config) error        { return nil }
func (v *fixedVendor) Partitions(Config) []string { return v.partitions }
func (*fixedVendor) CopyCapable()                 {}
func (v *fixedVendor) Link(env *Env) error {
	for _, m := range env.Mounted() {
		v.linked = append(v.linked, m.Name)
		for _, f := range env.Walk(m, 0) {
			if filepath.Base(f) == "fw_bcmdhd.bin" {
				env.Place(f, "vendor-name/"+filepath.Base(f))
			}
		}
	}
	return nil
}

func TestCustomVendor(t *testing.T) {
	v := &fixedVendor{partitions: []string{"persist"}}
	Register(v)
	defer func() { vendorMu.Lock(); delete(vendors, "fixed"); vendorMu.Unlock() }()

	cfg, _ := genericWorld(t)
	cfg.Vendor = "fixed"
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Only the partition the vendor asked for was mounted and offered.
	if len(res.Mounts) != 1 || res.Mounts[0].Name != "persist" || strings.Join(v.linked, ",") != "persist" {
		t.Errorf("mounts %+v, linked %v", res.Mounts, v.linked)
	}
	if got := read(t, filepath.Join(res.Target, "vendor-name/fw_bcmdhd.bin")); got != "second copy" {
		t.Errorf("placed %q", got)
	}

	// Config.Partitions replaces the vendor's list; Copy works with Env.Place.
	v.linked = nil
	cfg2, fake := genericWorld(t)
	cfg2.Vendor = "fixed"
	cfg2.Partitions = []string{"system"}
	cfg2.Copy = true
	res2, err := Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(res2.Target, "vendor-name/fw_bcmdhd.bin")); got != "WFW" {
		t.Errorf("placed %q", got)
	}
	if fi, _ := os.Lstat(filepath.Join(res2.Target, "vendor-name/fw_bcmdhd.bin")); fi == nil || !fi.Mode().IsRegular() {
		t.Error("not a copy")
	}
	if len(fake.unmounted) != 1 {
		t.Errorf("unmounted %v", fake.unmounted)
	}
}

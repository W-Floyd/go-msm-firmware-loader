package msmfw_test

import (
	"bytes"
	"testing"

	msmfw "github.com/W-Floyd/go-msm-firmware-loader"
)

// Only the exported surface: what another module (the Boot Manager) sees.
func TestExportedSurface(t *testing.T) {
	var _ msmfw.System = msmfw.NewSystem("/dev", "/proc")
	var logs []string
	res, err := msmfw.Run(msmfw.Config{
		Base:       t.TempDir() + "/x",
		SlotSuffix: "_a",
		Sys:        t.TempDir(), // no disks
		DryRun:     true,
		Logf:       func(f string, a ...any) { logs = append(logs, f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mounts) != 0 || !res.DryRun {
		t.Errorf("%+v", res)
	}
	if err := res.Close(); err != nil {
		t.Error(err)
	}
	if got := msmfw.DefaultPartitions("_a"); len(got) != 7 || got[3] != "fsg_a" {
		t.Errorf("%v", got)
	}

	var out, errOut bytes.Buffer
	if code := msmfw.Main([]string{"-no-such-flag"}, &out, &errOut); code != msmfw.ExitUsage {
		t.Errorf("exit %d", code)
	}
	if errOut.Len() == 0 || out.Len() != 0 {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
	if code := msmfw.Main([]string{"--dry-run", "--sysroot-not-a-flag"}, &out, &errOut); code != msmfw.ExitUsage {
		t.Errorf("exit %d", code)
	}
}

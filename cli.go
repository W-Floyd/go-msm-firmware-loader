package msmfw

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Exit codes of Main.
const (
	ExitOK    = 0 // loaded; some partitions may have failed to mount (see stderr), as with the script
	ExitError = 1 // nothing usable could be set up
	ExitUsage = 2
)

const usage = `usage: msm-firmware-loader [flags]

Mounts the firmware partitions of a Qualcomm device read-only under --base,
links their blobs into --base/target and points firmware_class.path at it.

--vendor picks the family of devices whose layout to use: qcom (the
default, as above), or generic, which any device can use and which --rule
implies. A vendor that knows a family's partitions and file layout is quicker
and cleverer; generic needs no such knowledge. Generic mounts every named
partition (or those of --partitions) read-only, finds the files the rules
name and places them in --base/target under the names the kernel's drivers
ask for, as links, or as copies with --copy (the partitions are then let go).
For example:

  --rule fw_bcmdhd.bin=brcm/brcmfmac4359-sdio.bin --rule 'bcm*.hcd=brcm/' --copy

`

// Main is the command line: args are os.Args[1:]. It returns the exit code
// and writes only to the writers it is given, so a larger program can embed
// it as a subcommand.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("msm-firmware-loader", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	var (
		base      = fs.String("base", DefaultBase, "directory to unfold the partitions into")
		noSetPath = fs.Bool("no-set-path", false, "do not write firmware_class.path")
		parts     = fs.String("partitions", "", "comma-separated partition names to mount, instead of the default list")
		supers    = fs.String("super-partitions", "", "comma-separated names that may hold dynamic partitions (default super,system)")
		slot      = fs.String("slot-suffix", "", "A/B slot suffix, e.g. _a (default: androidboot.slot_suffix from --cmdline)")
		cmdline   = fs.String("cmdline", "/proc/cmdline", "kernel command line file")
		extra     = fs.String("extra-path", "", "directory of blobs to link first (default: the current firmware_class.path)")
		noDM      = fs.Bool("no-dm", false, "loop devices only; never device-mapper")
		compat    = fs.Bool("compat", false, "behave exactly as the shell script does, bugs included")
		substr    = fs.Bool("substring-match", false, "match partition names as substrings of the lists (the script's way; --compat implies it)")
		exact     = fs.Bool("exact-match", false, "deprecated: exact matching is the default; this forces it under --compat")
		dryRun    = fs.Bool("dry-run", false, "print what would be mounted and change nothing")
		teardown  = fs.Bool("teardown", false, "unmount everything under --base (a test aid) and exit")
		quiet     = fs.Bool("q", false, "do not log progress")
		vendorArg = fs.String("vendor", "", "the device family's layout: "+strings.Join(Vendors(), ", ")+" (default qcom, or generic with --rule)")
		copyFiles = fs.Bool("copy", false, "generic vendor: copy the files into the target and unmount the partitions, instead of linking")
		maxDepth  = fs.Int("max-depth", 0, "generic vendor: directories below a partition's root a rule looks (default 6)")
	)
	var rules []Rule
	fs.Func("rule", "generic vendor: FIND=DST, a file name (or pattern) to find in the partitions and where it goes in the target; DST ending in / takes every match under its own name; repeatable", func(s string) error {
		r, err := ParseRule(s)
		if err != nil {
			return err
		}
		rules = append(rules, r)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return ExitOK
		}
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return ExitUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	logf := logWriter(stderr, "msm-firmware-loader: ")

	if *teardown {
		if err := Teardown(*base, ""); err != nil {
			logf("%v", err)
			return ExitError
		}
		return ExitOK
	}

	cfg := Config{
		Base: *base, NoSetPath: *noSetPath, NoDM: *noDM, ExactMatch: *exact, DryRun: *dryRun,
		Compat: *compat, SubstringMatch: *substr,
		Partitions: splitList(*parts), SuperPartitions: splitList(*supers),
		Vendor: *vendorArg, Rules: rules, Copy: *copyFiles, MaxDepth: *maxDepth,
	}
	if !*quiet {
		cfg.Logf = logf
	}
	if set["slot-suffix"] {
		cfg.SlotSuffix = *slot
	} else if b, err := os.ReadFile(*cmdline); err == nil {
		cfg.SlotSuffix = SlotSuffixFromCmdline(string(b))
	}
	if set["extra-path"] {
		cfg.ExtraPath = *extra
	} else {
		cfg.ExtraPath = CurrentFirmwarePath("")
	}

	res, err := Run(cfg)
	if err != nil {
		logf("%v", err)
		return ExitError
	}
	failed := 0
	for _, m := range res.Mounts {
		switch {
		case res.DryRun:
			fmt.Fprintf(stdout, "mount %s (%s: %s) on %s\n", m.Name, m.Via, m.Device, m.Dir)
		case m.Err != nil:
			failed++
		default:
			fmt.Fprintf(stdout, "mounted %s (%s: %s)\n", m.Name, m.Via, m.Device)
		}
	}
	if res.DryRun {
		fmt.Fprintf(stdout, "slot suffix %q; tmpfs on %s; would link blobs into %s\n", cfg.SlotSuffix, res.Base, res.Target)
		return ExitOK
	}
	if *vendorArg == "generic" || (*vendorArg == "" && len(rules) > 0) {
		fmt.Fprintf(stdout, "%d link(s), %d cop(ies) in %s; %d rule(s) found nothing; %d partition(s) not mounted; firmware_class.path set: %v\n",
			res.Links, res.Copied, res.Target, len(res.Missing), failed, res.PathSet)
		return ExitOK
	}
	fmt.Fprintf(stdout, "%d link(s) in %s; %d partition(s) not mounted; firmware_class.path set: %v\n",
		res.Links, res.Target, failed, res.PathSet)
	return ExitOK
}

func splitList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	if len(f) == 0 {
		return nil
	}
	return f
}

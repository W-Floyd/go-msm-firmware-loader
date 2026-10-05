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
		exact     = fs.Bool("exact-match", false, "match partition names against the lists exactly, not as substrings (the script's way)")
		dryRun    = fs.Bool("dry-run", false, "print what would be mounted and change nothing")
		teardown  = fs.Bool("teardown", false, "unmount everything under --base (a test aid) and exit")
		quiet     = fs.Bool("q", false, "do not log progress")
	)
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
		Partitions: splitList(*parts), SuperPartitions: splitList(*supers),
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

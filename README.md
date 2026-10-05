# go-msm-firmware-loader

A static Go port of postmarketOS's
[`msm-firmware-loader`](https://gitlab.postmarketos.org/postmarketOS/msm-firmware-loader)
shell script (1.8.0, plus our patch that exposes the `fsg` partition for
tqftpserv). It mounts the firmware partitions of a Qualcomm device read-only,
links their blobs into one directory, and points the kernel's
`firmware_class.path` at it.

It is a library first (`package msmfw`, so the Boot Manager can run it inside
its own binary) with a thin command, `cmd/msm-firmware-loader`, around it.

## Why

The script needs about fifteen programs (sed, xargs, cut, head, find, ...),
`make-dynpart-mappings` (and libdevmapper, libblkid, libmd), and `qbootctl`.
`qbootctl` opens `/dev/disk/by-partlabel`, which only udev creates: without
those links it finds no A/B slot, and the script looks for `modem` instead of
`modem_a` and mounts only `persist` and `sp`.

This does the same with none of that, so it can run as the real loader on
postmarketOS and also in an initramfs with no busybox, no udev and no
device-mapper:

- the slot suffix is `androidboot.slot_suffix=` from `/proc/cmdline`;
- partitions are found by `PARTNAME` in `/sys/block/*/*/uevent`;
- a logical partition of `super` (Android dynamic partitions) is parsed from
  the liblp metadata and, when it is one extent (the usual case), mounted
  through a **read-only loop device** with offset and size limit. Only a
  partition in several extents needs **dm-linear**, by ioctl, and only if the
  kernel has device-mapper (`--no-dm` never uses it);
- file systems are told from their superblock (erofs, ext4, f2fs, squashfs,
  xfs, vfat), with a try-everything fallback as busybox `mount` has;
- all mounts, loop devices and symlinks are made by syscall.

Nothing in it is specific to one device.

## Command line

```
msm-firmware-loader [flags]
  --base DIR          where to unfold the partitions (default /run/msm-firmware-loader)
  --no-set-path       do not write /sys/module/firmware_class/parameters/path
  --partitions a,b    mount these partitions instead of the default list
  --super-partitions  names that may hold dynamic partitions (default super,system)
  --slot-suffix _a    default: androidboot.slot_suffix from --cmdline
  --extra-path DIR    blobs to link first (default: the current firmware_class.path)
  --no-dm             loop devices only
  --compat            behave exactly as the shell script does, bugs included
  --substring-match   match names as substrings (implied by --compat)
  --exact-match       deprecated: exact is the default; forces it under --compat
  --dry-run           print what would be mounted; change nothing
  --teardown          unmount everything under --base, and exit
  -q                  do not log progress
```

Exit status: `0` loaded (a partition that would not mount is logged and
skipped, as in the script, so check the log or `Result.Mounts`); `1` nothing
usable could be set up (no tmpfs, no directory, `--base` already mounted, path
not writable); `2` usage. Log lines go to stderr, prefixed
`msm-firmware-loader:`; the summary goes to stdout.

## As a library

```go
res, err := msmfw.Run(msmfw.Config{SlotSuffix: "_a", NoSetPath: true})
if err != nil { /* res may still be Closed */ }
defer res.Close() // optional: undo the mounts; the usual thing is to leave them
```

The API (no globals, no `os.Exit`, no flags, no direct stdout; callable twice):

```go
type Config struct { Base, SlotSuffix string; Partitions, SuperPartitions []string
    Compat, SubstringMatch, ExactMatch bool; ExtraPath string; NoDM, NoSetPath, DryRun bool
    Sys, Dev, Proc string; Logf func(string, ...any); System System }
func Run(cfg Config) (*Result, error)
type Result struct { Base, Target string; Mounts []Mount; Links int; PathSet, DryRun bool }
func (*Result) Close() error
type Mount struct { Name, Dir, Device, Via string; Err error }
func Main(args []string, stdout, stderr io.Writer) int // the CLI, for embedding
func Teardown(base, proc string) error
func SlotSuffixFromCmdline(cmdline string) string
func CurrentFirmwarePath(sysRoot string) string
func DefaultPartitions(slotSuffix string) []string
func DefaultSuperPartitions() []string
type System interface { ... } ; func NewSystem(dev, proc string) System
```

`System` is the mount layer; tests replace it (`msmfw_test.go` does, with
fixtures). Only `golang.org/x/sys` is required.

### From another module (the Boot Manager)

The module is not published. Use a `go.work` next to the consumer, or a
`replace`:

```
# ~/bootmanager/go.work
go 1.26.0
use (
	./bootmanager
	../go-msm-firmware-loader
)
```

or in `bootmanager/go.mod`:
`replace github.com/W-Floyd/go-msm-firmware-loader => ../../go-msm-firmware-loader`
(with a matching `require`). Then, as a subcommand:

```go
case "firmware-loader":
	os.Exit(msmfw.Main(os.Args[2:], os.Stdout, os.Stderr))
```

## Fidelity to the script, and where it differs

Same, step by step: the partition lists (with the `fsg` addition); the
tmpfs with `mnt/` and `target/`; blobs of `mnt/*/image/*` then
`mnt/*/firmware/*` in glob order, skipped when `name.*` is already in the
target, each bringing the files in its directory that start with its name;
the preselected-path blobs first; `sns.reg`; `WCNSS_qcom_wlan_nv.bin` and its
`wlan/prima` copy; `venus-x` and the `venus-*`/`vpu-*` aliases; bluetooth
files under `qca/`; the `fsg` link; `.mdt` to `.mbn` links; the devicetree
`firmware-name` prefix link; finally the path write. A failed mount leaves its
empty directory in `mnt/`, which stops a second attempt at the same name, as
in the script. Link errors ("File exists", duplicate zap shaders) are logged
and ignored.

### Default and `-compat`

The script has bugs. By default they are fixed. `-compat` (`Config.Compat`)
keeps the script's behaviour exactly: the link tree, the `mnt/` directories
and the logged errors are what the script makes today.

| # | Behaviour | Default | `-compat` | Why |
|---|-----------|---------|-----------|-----|
| 1 | Slot suffix | `androidboot.slot_suffix` from the command line; a warning is logged if it is empty but A/B partitions (`*_a`, `*_b`) exist | the same, without the warning | The script asks `qbootctl` if it is installed, and `qbootctl` needs udev's `/dev/disk/by-partlabel`; before udev it fails and the suffix is silently empty, so only `persist` and `sp` mount. This never needs `qbootctl` or udev links. **Compat cannot reproduce `qbootctl` failing** (that is a property of the environment, not of the logic); it behaves as the script does when `qbootctl` is absent. |
| 2 | tmpfs on `--base` | the directory is made, the tmpfs is mounted for real | a tmpfs only if `--base` is already a directory; otherwise logs `mounting none on ... failed: No such file or directory` and goes on, in a plain directory, as the script does on every boot | The script never creates the directory, so its isolation never exists. In compat the plain directory is removed again by `Close`. |
| 3 | Partition-name matching | whole entries of the lists | substring of the lists written out as one string, so `sp` is tried because `dsp_a` contains it (and fails: "wrong fs type") | A substring test is a typo-class bug; `sp` is not firmware. `-substring-match` gives it without the rest of compat. `-exact-match` is deprecated: it is the default, and forces exact matching under compat. |
| 4 | Dangling link in the preselected path | skipped, the rest is linked | the loop ends there; later blobs are dropped | The script's `break` was meant for the unmatched glob. |
| 5 | Unguarded globs | no link | an empty `bluetooth*/image/` makes a link named `*` in `qca/` | The only unguarded glob is that one (the `venus.*` loop only runs when `venus.mdt` matches it). |
| 5b | `bluetooth`/`fsg` partition that failed to mount | not linked | linked: the script only checks the (empty) mount directory | A failed mount is not a partition. |
| 6 | `.mdt` to `.mbn` links, and other "File exists" | an existing `.mbn` is left alone silently; an identical existing link is no error (a different one is logged) | tries every one and logs `file exists` for each | Noise hides real errors. |
| 7 | Dynamic-partition metadata slot | the slot of the A/B suffix (`_a` is 0, `_b` is 1, as Android maps it); a partition not listed with extents there is looked for in the other slots, and the log says so | slot 0 only, as `make-dynpart-mappings` ("Only the first slot is mapped"; that message is the tool's and is not printed). A listed partition without extents is a failed mount | Slot 0 is wrong on `_b`. Only the listed partitions are mapped, not all of `super`. |
| 8 | Run twice | refused with `ErrLoaded` while `--base` is a mount point; fine again after `Close`/`--teardown` | allowed, and stacks another tmpfs over the first, hiding its mounts | Stacking is only ever a mistake. |
| 9 | Devicetree `firmware-name` prefix | every distinct directory of every string of every property, sorted; the tree is linked under each; bare names (no directory) are ignored | the first property `find` returns (readdir order), its first word; a bare name gives `.` and a link `target/target` | A device has several prefixes. |

Unchanged in both: the disks scanned are `mmcblk*` and `sd*`, as in the script.

`msm-firmware-loader-unpack.sh` (gunzip `*.gz` blobs in the target) is not
ported: it runs after this and is a separate service.

## Limits

- dm-linear (a partition in several extents) is implemented and unit-tested
  for its wire format, but has not been run on hardware: the phone it was
  tested on has only single-extent partitions.
- Only linear extents on `super` itself are supported (as for the loop path);
  a partition on another block device in the metadata is skipped.
- Linux, root, and the kernel's loop device (`/dev/loop-control`) are required
  for dynamic partitions.

## Verification

`tools/compare-targets.sh BASE_A BASE_B` lists two trees without following
symlinks, normalises each base path in the link targets, and reports entries
missing, extra or differing (busybox-safe: `find`, `readlink`, `comm`).

On a Moto G Play 2024 (mainline 7.3-rc4, slot `_a`), run beside the live
script with `--base /run/mfl-go --no-set-path --extra-path /lib/firmware/postmarketos`
(without `--extra-path` the live script's own target is linked, and the
`ath10k` link from that directory is the only other difference):

- `-compat`: 323 entries each, including the seven `mnt/` names and the `sp`
  mount attempt; no entry missing, extra or differing.
- default: 321 entries, nothing extra or differing. Missing: `mnt/sp` (3) and
  `target/target`, the self-referential link the script makes because the
  first `firmware-name` property on this device is a bare file name (9). The
  other log differences: no "file exists" lines (6), and a real tmpfs (2).
  This device's properties are all bare names, so no prefix link is made in
  either mode.

## Test fixtures

No binary data is committed. The tests build their `super` (dynamic-partition
metadata) image in process: `internal/liblp/fixture.go` writes a version 10.2
header with the geometry of a recent phone (64 KiB metadata slots, three
slots, 4096-byte blocks) and the usual logical partitions as single linear
extents, one after another from sector 2048. The names are Android's, the
sector numbers are made up. To look at the image with other tools:

```sh
go generate ./internal/liblp   # writes internal/liblp/testdata/super-synthetic.img (git-ignored)
```

## Licence

MIT, as upstream. `internal/liblp` is from the Boot Manager's firmware package.

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
  --exact-match       match partition names exactly, not as substrings
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
    ExactMatch bool; ExtraPath string; NoDM, NoSetPath, DryRun bool
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

Decisions:

- **Substring match is kept.** The script tests whether the partition name is
  a substring of the whole list written as one string, so `sp` matches
  because `dsp_a` contains it (it is then tried and, on the Moto G Play 2024,
  fails to mount: "wrong fs type"; the loader carries on). That is what runs
  today, so it is the default. `--exact-match` / `Config.ExactMatch` makes it
  whole-entry matching, which would not try `sp`.
- **Metadata slot follows the A/B slot.** The script's `make-dynpart-mappings`
  maps only the first metadata slot ("This looks like metadata for retrofit
  partitions. Only the first slot is mapped"; that message is the tool's and
  is not reproduced). Here slot `_a` is metadata slot 0 (identical on `_a`)
  and `_b` is slot 1, which is what Android itself maps.
- **Only partitions on the list are mapped**, not all of `super`.
- **Not reproduced on purpose:** a dangling symlink in the preselected path
  is skipped, where the script's `break` stops linking; an empty
  `bluetooth` mount does not create a link literally named `*` in `qca/`.
- **Refuses to load twice** into the same `--base` (the script stacks another
  tmpfs).
- **Disks** are `mmcblk*` and `sd*` only, as in the script.

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
script with `--base /run/mfl-go --no-set-path --extra-path /lib/firmware/postmarketos`,
the result was 323 entries each (the tree's links and directories, and the
seven `mnt/` names), and the comparison reported no differences. Without
`--extra-path` the only difference is the `ath10k` link that comes from that
directory.

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

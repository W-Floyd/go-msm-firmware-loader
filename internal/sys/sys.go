// Package sys is the Linux side of the loader: mounts, loop devices and
// device-mapper, all by syscall, with no external program and no udev.
package sys

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// System does the privileged work for a Dev directory (normally /dev).
type System struct {
	Dev  string
	Proc string // /proc, for the list of file systems the kernel has
}

// MountTmpfs mounts the scratch tmpfs: the loader only makes symlinks, and
// it runs before anything writable may exist.
func (s System) MountTmpfs(dir string) error {
	return unix.Mount("none", dir, "tmpfs", unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_NOSUID, "mode=755")
}

// Unmount unmounts dir, which a read-only mount here is never busy for.
func (s System) Unmount(dir string) error { return unix.Unmount(dir, 0) }

// MountRO mounts a block device read-only, finding the file system itself:
// busybox mount does that for the shell script.
func (s System) MountRO(dev, dir string) error {
	flags := uintptr(unix.MS_RDONLY | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_NOSUID)
	if fs := detectFS(dev); fs != "" {
		return unix.Mount(dev, dir, fs, flags, readOnlyData(fs))
	}
	// Nothing we know the superblock of: try what the kernel has, as busybox
	// does, and report the last failure.
	last := errors.New("no file system the kernel knows")
	for _, fs := range s.blockFilesystems() {
		err := unix.Mount(dev, dir, fs, flags, readOnlyData(fs))
		if err == nil {
			return nil
		}
		last = err
	}
	return fmt.Errorf("wrong fs type, bad option, bad superblock (%w)", last)
}

// blockFilesystems is /proc/filesystems without the "nodev" ones.
func (s System) blockFilesystems() []string {
	f, err := os.Open(filepath.Join(s.Proc, "filesystems"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 1 {
			out = append(out, fields[0])
		}
	}
	return out
}

// readOnlyData keeps a read-only mount from replaying a journal, which would
// be a write.
func readOnlyData(fstype string) string {
	switch fstype {
	case "ext2", "ext3", "ext4":
		return "noload"
	case "f2fs", "xfs":
		return "norecovery"
	}
	return ""
}

// detectFS tells the file system on dev from its superblock, for the ones
// Android devices use. u-root's detection does not know erofs, which is why
// this exists. "" means not recognised.
func detectFS(dev string) string {
	f, err := os.Open(dev)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 2048)
	n, _ := f.ReadAt(buf, 0)
	buf = buf[:n]
	le32 := func(at int) uint32 {
		if at+4 > len(buf) {
			return 0
		}
		return binary.LittleEndian.Uint32(buf[at:])
	}
	switch {
	case le32(1024) == 0xE0F5E1E2:
		return "erofs"
	case le32(1024) == 0xF2F52010:
		return "f2fs"
	case le32(0) == 0x73717368: // "hsqs"
		return "squashfs"
	case string(buf[:min(4, len(buf))]) == "XFSB":
		return "xfs"
	case len(buf) > 1082 && binary.LittleEndian.Uint16(buf[1080:]) == 0xEF53:
		return "ext4" // the ext4 driver also reads ext2 and ext3
	case len(buf) > 512 && buf[510] == 0x55 && buf[511] == 0xAA &&
		(string(buf[54:59]) == "FAT16" || string(buf[82:87]) == "FAT32"):
		return "vfat"
	}
	return ""
}

// LoopOver makes a read-only loop device over [off, off+size) of backing.
// The release func closes our hold on it: once it is mounted, the mount keeps
// it, and autoclear frees it at unmount. If the mount failed, releasing frees
// it at once.
func (s System) LoopOver(backing string, off, size uint64) (dev string, release func(), err error) {
	ctl, err := os.OpenFile(filepath.Join(s.Dev, "loop-control"), os.O_RDWR, 0)
	if err != nil {
		return "", nil, fmt.Errorf("loop-control: %w", err)
	}
	defer ctl.Close()
	nr, err := unix.IoctlRetInt(int(ctl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", nil, fmt.Errorf("no free loop device: %w", err)
	}
	dev = filepath.Join(s.Dev, fmt.Sprintf("loop%d", nr))
	// devtmpfs makes the node a moment after the device exists.
	var lf *os.File
	for i := 0; i < 40; i++ {
		if lf, err = os.OpenFile(dev, os.O_RDWR, 0); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return "", nil, err
	}
	bf, err := os.Open(backing)
	if err != nil {
		lf.Close()
		return "", nil, err
	}
	defer bf.Close()
	cfg := unix.LoopConfig{Fd: uint32(bf.Fd()), Info: unix.LoopInfo64{
		Offset: off, Sizelimit: size,
		Flags: unix.LO_FLAGS_READ_ONLY | unix.LO_FLAGS_AUTOCLEAR,
	}}
	if err := unix.IoctlLoopConfigure(int(lf.Fd()), &cfg); err != nil {
		lf.Close()
		return "", nil, fmt.Errorf("configure %s over %s: %w", dev, backing, err)
	}
	return dev, func() { lf.Close() }, nil
}

// MountPoints lists the mount points under (and including) dir, deepest
// first, from mountinfo, so a teardown can unmount them in order.
func MountPoints(mountinfo, dir string) ([]string, error) {
	f, err := os.Open(mountinfo)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescape(fields[4])
		if mp == dir || strings.HasPrefix(mp, dir+"/") {
			out = append(out, mp)
		}
	}
	// mountinfo is in mount order, so the reverse unmounts children first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, sc.Err()
}

// unescape undoes mountinfo's \040-style octal escapes.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			ok := true
			for _, c := range []byte(s[i+1 : i+4]) {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

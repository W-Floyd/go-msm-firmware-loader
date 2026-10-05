package sys

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

// A minimal device-mapper client: linear, read-only tables, which is all a
// dynamic partition in several pieces needs. It is only the fallback for what
// a loop device cannot do (one device from several extents), and only for a
// kernel with CONFIG_BLK_DEV_DM and dm-linear.

const (
	dmIoctlType = 0xfd
	dmStructLen = 312 // sizeof(struct dm_ioctl)
	dmVersion   = 4

	dmCmdDevCreate  = 3
	dmCmdDevRemove  = 4
	dmCmdDevSuspend = 6 // without the suspend flag, a resume
	dmCmdDevStatus  = 7
	dmCmdTableLoad  = 9

	dmReadOnlyFlag = 1 << 0
	dmSuspendFlag  = 1 << 1
)

// HasDM is whether the kernel has device-mapper at all.
func (s System) HasDM() bool {
	_, err := os.Stat(filepath.Join(s.Dev, "mapper", "control"))
	return err == nil
}

// dmRequest is a struct dm_ioctl followed by data, for one ioctl.
func dmRequest(name string, flags uint32, targets, params []byte, count uint32) []byte {
	b := make([]byte, dmStructLen, dmStructLen+len(targets)+len(params))
	le := binary.LittleEndian
	le.PutUint32(b[0:], dmVersion)
	le.PutUint32(b[16:], dmStructLen) // data_start
	le.PutUint32(b[20:], count)       // target_count
	le.PutUint32(b[28:], flags)
	copy(b[48:175], name) // name[128], kept NUL terminated
	b = append(b, targets...)
	b = append(b, params...)
	le.PutUint32(b[12:], uint32(len(b))) // data_size
	return b
}

// dmLinearTable is the table data for read-only linear extents of one device,
// one dm_target_spec and its parameter string each, 8-byte aligned.
func dmLinearTable(majmin string, ex []liblp.Extent) (data []byte) {
	var logical uint64
	for _, e := range ex {
		params := fmt.Sprintf("%s %d", majmin, e.Start)
		spec := make([]byte, 40)
		le := binary.LittleEndian
		le.PutUint64(spec[0:], logical)
		le.PutUint64(spec[8:], e.Sectors)
		copy(spec[24:40], "linear")
		body := append(spec, append([]byte(params), 0)...)
		for len(body)%8 != 0 {
			body = append(body, 0)
		}
		le.PutUint32(body[20:], uint32(len(body))) // next, from this spec
		data = append(data, body...)
		logical += e.Sectors
	}
	return data
}

func (s System) dmIoctl(ctl *os.File, cmd uintptr, req []byte) error {
	nr := 3<<30 | uintptr(dmStructLen)<<16 | dmIoctlType<<8 | cmd
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, ctl.Fd(), nr, uintptr(unsafe.Pointer(&req[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

// DMCreate makes the read-only linear device name over the extents of
// superDev, and returns its node.
func (s System) DMCreate(superDev, name string, ex []liblp.Extent) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(superDev, &st); err != nil {
		return "", err
	}
	majmin := fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))
	ctl, err := os.OpenFile(filepath.Join(s.Dev, "mapper", "control"), os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer ctl.Close()

	if err := s.dmIoctl(ctl, dmCmdDevCreate, dmRequest(name, 0, nil, nil, 0)); err != nil {
		return "", fmt.Errorf("dm create %s: %w", name, err)
	}
	fail := func(what string, err error) (string, error) {
		s.dmIoctl(ctl, dmCmdDevRemove, dmRequest(name, 0, nil, nil, 0))
		return "", fmt.Errorf("dm %s %s: %w", what, name, err)
	}
	table := dmLinearTable(majmin, ex)
	if err := s.dmIoctl(ctl, dmCmdTableLoad, dmRequest(name, dmReadOnlyFlag, table, nil, uint32(len(ex)))); err != nil {
		return fail("load table of", err)
	}
	if err := s.dmIoctl(ctl, dmCmdDevSuspend, dmRequest(name, 0, nil, nil, 0)); err != nil {
		return fail("resume", err)
	}
	status := dmRequest(name, 0, nil, nil, 0)
	if err := s.dmIoctl(ctl, dmCmdDevStatus, status); err != nil {
		return fail("status of", err)
	}
	minor := unix.Minor(binary.LittleEndian.Uint64(status[40:]))
	// devtmpfs makes /dev/dm-N a moment after the device resumes.
	node := filepath.Join(s.Dev, fmt.Sprintf("dm-%d", minor))
	for i := 0; i < 40; i++ {
		if _, err := os.Stat(node); err == nil {
			return node, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fail("find node of", fmt.Errorf("%s did not appear", node))
}

// DMRemove removes a device DMCreate made.
func (s System) DMRemove(name string) error {
	ctl, err := os.OpenFile(filepath.Join(s.Dev, "mapper", "control"), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer ctl.Close()
	return s.dmIoctl(ctl, dmCmdDevRemove, dmRequest(name, 0, nil, nil, 0))
}

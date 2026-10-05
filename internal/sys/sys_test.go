package sys

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

func TestDMLinearTable(t *testing.T) {
	tab := dmLinearTable("259:26", []liblp.Extent{{Start: 20971520, Sectors: 100}, {Start: 4096, Sectors: 50}})
	le := binary.LittleEndian
	// "259:26 20971520" and its NUL make 16 bytes: 40 + 16.
	if le.Uint32(tab[20:]) != 56 {
		t.Fatalf("next = %d", le.Uint32(tab[20:]))
	}
	second := tab[56:]
	if le.Uint64(second[0:]) != 100 || le.Uint64(second[8:]) != 50 {
		t.Errorf("second spec starts at %d, %d long", le.Uint64(second[0:]), le.Uint64(second[8:]))
	}
	if string(tab[24:30]) != "linear" || string(tab[40:55]) != "259:26 20971520" {
		t.Errorf("first spec: %q %q", tab[24:40], tab[40:56])
	}
	if len(tab)%8 != 0 {
		t.Errorf("table is %d bytes, not aligned", len(tab))
	}
	r := dmRequest("vendor_a", dmReadOnlyFlag, tab, nil, 2)
	if le.Uint32(r[12:]) != uint32(len(r)) || le.Uint32(r[16:]) != dmStructLen || string(r[48:56]) != "vendor_a" {
		t.Error("request header")
	}
}

func TestMountPoints(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mountinfo")
	os.WriteFile(f, []byte(`1 0 0:1 / /run rw - tmpfs none rw
2 1 0:2 / /run/mfl rw - tmpfs none rw
3 2 7:0 / /run/mfl/mnt/a\040b ro - erofs /dev/loop0 ro
4 2 7:1 / /run/mfl/mnt/c ro - erofs /dev/loop1 ro
5 1 0:3 / /run/mflx rw - tmpfs none rw
`), 0o644)
	got, err := MountPoints(f, "/run/mfl")
	want := []string{"/run/mfl/mnt/c", "/run/mfl/mnt/a b", "/run/mfl"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("%v, %v; want %v", got, err, want)
	}
}

func TestDetectFS(t *testing.T) {
	img := make([]byte, 4096)
	binary.LittleEndian.PutUint32(img[1024:], 0xE0F5E1E2)
	f := filepath.Join(t.TempDir(), "img")
	os.WriteFile(f, img, 0o644)
	if got := detectFS(f); got != "erofs" {
		t.Errorf("erofs: %q", got)
	}
	clear(img)
	binary.LittleEndian.PutUint16(img[1080:], 0xEF53)
	os.WriteFile(f, img, 0o644)
	if got := detectFS(f); got != "ext4" {
		t.Errorf("ext: %q", got)
	}
	clear(img)
	os.WriteFile(f, img, 0o644)
	if got := detectFS(f); got != "" {
		t.Errorf("zeros: %q", got)
	}
}

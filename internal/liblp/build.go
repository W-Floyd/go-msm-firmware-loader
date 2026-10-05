package liblp

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
)

// Options are the geometry of a synthesised super device.
type Options struct {
	MaxSize   uint32 // metadata_max_size: bytes per metadata slot (default 4096)
	BlockSize uint32 // logical_block_size (default 4096)
}

// Build lays out a super image the way liblp does, with linear extents on
// block device 0: slots[i] are the partitions of metadata slot i. It is for
// tests that need a super with several slots or extents, or damage; the
// fixture below is the one shaped like a phone's.
func Build(slots [][]Partition) []byte { return BuildWith(Options{}, slots) }

// BuildWith is Build with a geometry. The image holds the reserved area, both
// geometry copies and the primary metadata slots; the backup copies liblp
// keeps at the end of super are not written, as nothing here reads them.
func BuildWith(o Options, slots [][]Partition) []byte {
	if o.MaxSize == 0 {
		o.MaxSize = 4096
	}
	if o.BlockSize == 0 {
		o.BlockSize = 4096
	}
	le := binary.LittleEndian
	maxSize := int(o.MaxSize)
	img := make([]byte, geometryOffset+2*geometrySize+len(slots)*maxSize)

	g := img[geometryOffset : geometryOffset+geometrySize]
	le.PutUint32(g[0:], geometryMagic)
	le.PutUint32(g[4:], 52)
	le.PutUint32(g[40:], o.MaxSize)
	le.PutUint32(g[44:], uint32(len(slots)))
	le.PutUint32(g[48:], o.BlockSize)
	sum := sha256.Sum256(g[:52])
	copy(g[8:], sum[:])
	copy(img[geometryOffset+geometrySize:], g) // the backup geometry

	// The one block device is super itself, as long as its last extent.
	var end uint64
	for _, parts := range slots {
		for _, p := range parts {
			for _, x := range p.Extents {
				end = max(end, x.End())
			}
		}
	}
	const mib = 1 << 20
	superBytes := (end*sectorSize + mib - 1) / mib * mib

	for n, parts := range slots {
		var ptab, etab []byte
		for _, p := range parts {
			e := make([]byte, 52) // name, attributes, first extent, extents, group
			copy(e, p.Name)
			le.PutUint32(e[36:], 1) // LP_PARTITION_ATTR_READONLY
			le.PutUint32(e[40:], uint32(len(etab)/24))
			le.PutUint32(e[44:], uint32(len(p.Extents)))
			ptab = append(ptab, e...)
			for _, x := range p.Extents {
				xe := make([]byte, 24) // sectors, type, data, source
				le.PutUint64(xe[0:], x.Sectors)
				le.PutUint32(xe[8:], targetLinear)
				le.PutUint64(xe[12:], x.Start)
				etab = append(etab, xe...)
			}
		}
		gtab := make([]byte, 48) // one group, "default", no size limit
		copy(gtab, "default")
		btab := make([]byte, 64) // one block device: super
		le.PutUint64(btab[0:], 2048)
		le.PutUint32(btab[8:], mib)
		le.PutUint64(btab[16:], superBytes)
		copy(btab[24:60], "super")

		tables := append(append(append(append([]byte(nil), ptab...), etab...), gtab...), btab...)
		h := make([]byte, 256)
		le.PutUint32(h[0:], headerMagic)
		le.PutUint16(h[4:], 10)
		le.PutUint16(h[6:], 2)
		le.PutUint32(h[8:], 256)
		le.PutUint32(h[44:], uint32(len(tables)))
		tsum := sha256.Sum256(tables)
		copy(h[48:], tsum[:])
		put := func(at int, off, count, size int) {
			le.PutUint32(h[at:], uint32(off))
			le.PutUint32(h[at+4:], uint32(count))
			le.PutUint32(h[at+8:], uint32(size))
		}
		put(80, 0, len(parts), 52)
		put(92, len(ptab), len(etab)/24, 24)
		put(104, len(ptab)+len(etab), 1, 48)
		put(116, len(ptab)+len(etab)+len(gtab), 1, 64)
		hsum := sha256.Sum256(h)
		copy(h[12:], hsum[:])
		if len(h)+len(tables) > maxSize {
			panic("liblp.Build: metadata does not fit the slot")
		}
		off := geometryOffset + 2*geometrySize + n*maxSize
		copy(img[off:], h)
		copy(img[off+len(h):], tables)
	}
	return img
}

// SlotCount is how many metadata slots the geometry says there are.
func SlotCount(r io.ReaderAt) (int, error) {
	g, err := readGeometry(r)
	return int(g.slots), err
}

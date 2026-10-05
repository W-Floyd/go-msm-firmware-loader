package liblp

// A synthetic super, shaped like a recent Qualcomm phone's: geometry with a
// 64 KiB metadata slot, three slots and 4096-byte blocks, a version 10.2
// header, and the logical partitions of a virtual-A/B device. The first
// partition starts at sector 2048 (1 MiB in), the others follow one another,
// and each is a single linear extent. The other slot's partitions are listed
// without extents, as with virtual A/B.
//
// Nothing here was read off a device: the names are Android's usual ones and
// the sector numbers are made up.
var (
	FixtureProductA   = Extent{Start: 2048, Sectors: 6291456}
	FixtureSystemA    = Extent{Start: 6293504, Sectors: 2097152}
	FixtureSystemDLKM = Extent{Start: 8390656, Sectors: 16384}
	FixtureSystemExt  = Extent{Start: 8407040, Sectors: 524288}
	FixtureVendorA    = Extent{Start: 8931328, Sectors: 1048576}
	FixtureVendorDLKM = Extent{Start: 9979904, Sectors: 32768}
)

// FixtureLayout is the fixture's logical partitions.
func FixtureLayout() []Partition {
	one := func(name string, e Extent) Partition { return Partition{Name: name, Extents: []Extent{e}} }
	return []Partition{
		one("product_a", FixtureProductA), {Name: "product_b"},
		one("system_a", FixtureSystemA), {Name: "system_b"},
		one("system_dlkm", FixtureSystemDLKM),
		one("system_ext", FixtureSystemExt),
		one("vendor_a", FixtureVendorA), {Name: "vendor_b"},
		one("vendor_dlkm", FixtureVendorDLKM),
	}
}

// FixtureSuper is the head of a super device with FixtureLayout in all three
// metadata slots, 12 KiB plus three times 64 KiB.
func FixtureSuper() []byte {
	l := FixtureLayout()
	return BuildWith(Options{MaxSize: 65536, BlockSize: 4096}, [][]Partition{l, l, l})
}

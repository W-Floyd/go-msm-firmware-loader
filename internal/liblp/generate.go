package liblp

// The tests build their super images in memory and need nothing on disk.
// This writes the fixture to testdata/ (git-ignored) to look at with
// hexdump or lpdump-style tools.
//
//go:generate go run ./cmd/liblpgen -o testdata

// Command liblpgen writes the synthetic super image the tests use to a
// directory, for inspection with hexdump or other liblp tools:
//
//	go run ./cmd/liblpgen -o testdata
//
// The tests do not need it: they build the same image in memory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/W-Floyd/go-msm-firmware-loader/internal/liblp"
)

func main() {
	out := flag.String("o", "testdata", "directory to write the image to")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "liblpgen:", err)
		os.Exit(1)
	}
	p := filepath.Join(*out, "super-synthetic.img")
	if err := os.WriteFile(p, liblp.FixtureSuper(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "liblpgen:", err)
		os.Exit(1)
	}
	fmt.Println(p)
}

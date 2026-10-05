// Command msm-firmware-loader is the msmfw package as a program.
package main

import (
	"os"

	msmfw "github.com/W-Floyd/go-msm-firmware-loader"
)

func main() {
	os.Exit(msmfw.Main(os.Args[1:], os.Stdout, os.Stderr))
}

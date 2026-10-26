//go:build ignore

// Generate one noisy demo receipt image and one blank non-bill image.
//
//	go run scripts/genpng.go bill.png blank.png
package main

import (
	"fmt"
	"os"

	"github.com/Oasis1i/bill-snap/internal/testimg"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/genpng.go bill.png blank.png")
		os.Exit(2)
	}
	noisy, err := testimg.NoisyPNG(128, 128, 1)
	if err != nil {
		fatal(err)
	}
	blank, err := testimg.SolidPNG(80, 80)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(os.Args[1], noisy, 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(os.Args[2], blank, 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

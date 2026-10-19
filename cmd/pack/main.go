// Command pack gzips a file at maximum compression. The build scripts use it
// to shrink the program before it is embedded in the installer.
//
//	go run ./cmd/pack <input> <output.gz>
package main

import (
	"compress/gzip"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pack <input> <output.gz>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.Create(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := zw.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

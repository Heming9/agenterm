// agenterm-pack prepares deterministic release downloads and the raw bundle.
package main

import (
	"agenterm/internal/installer"
	"flag"
	"log"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("input", "dist", "directory containing the six CLI binaries")
	output := flag.String("output", "dist", "directory for archives and SHA256SUMS")
	bundle := flag.String("bundle", "clients.tar", "uncompressed bundle output")
	flag.Parse()
	source := os.DirFS(*input)
	files, err := installer.BuildDownloads(source)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*output, 0755); err != nil {
		log.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(*output, name), data, 0644); err != nil {
			log.Fatal(err)
		}
	}
	f, err := os.Create(*bundle)
	if err != nil {
		log.Fatal(err)
	}
	if err := installer.WriteBundle(f, source); err != nil {
		f.Close()
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

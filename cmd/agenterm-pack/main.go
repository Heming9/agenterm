// agenterm-pack prepares deterministic CLI downloads and the unified components.zip.
package main

import (
	"agenterm/internal/components"
	"agenterm/internal/installer"
	"agenterm/internal/p2p"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("input", "dist", "directory containing the six CLI binaries")
	output := flag.String("output", "dist", "directory for archives and SHA256SUMS")
	bundle := flag.String("bundle", "components.zip", "unified components.zip output")
	peerAssets := flag.String("p2p-assets", "", "directory containing verified native P2P archives and manifest.json")
	flag.Parse()
	source := os.DirFS(*input)
	files, err := installer.BuildDownloads(source)
	if err != nil {
		log.Fatal(err)
	}
	manifest, err := p2p.Load(*peerAssets)
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
	all := map[string][]byte{}
	for name, data := range files {
		all["client-cli/"+name] = data
	}
	metadata, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	all["p2p-cli/manifest.json"] = append(metadata, '\n')
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(*peerAssets, artifact.File))
		if err != nil {
			log.Fatal(err)
		}
		all["p2p-cli/"+artifact.File] = data
	}
	if err := writeBundle(*bundle, all); err != nil {
		log.Fatal(err)
	}
}

// Publish only complete archives, preserving the previous bundle on failure.
func writeBundle(path string, files map[string][]byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".components-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := components.Pack(f, files); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0644); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

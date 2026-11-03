// agenterm-pack builds the solid component bundle and deterministic XZ downloads.
package main

import (
	"agenterm/internal/components"
	"agenterm/internal/p2p"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("input", "dist", "directory containing six raw CLI binaries")
	output := flag.String("output", "dist", "directory for release download variants and raw SHA256SUMS")
	bundle := flag.String("bundle", "components.tar.xz", "solid components.tar.xz output")
	peerAssets := flag.String("p2p-assets", "", "directory containing verified native P2P executables and manifest.json")
	prepare := flag.String("prepare-bundle", "", "prepare an existing bundle with the runtime encoder instead of packing")
	cache := flag.String("cache-dir", "", "private cache directory for -prepare-bundle")
	flag.Parse()
	if *prepare != "" {
		data, err := os.ReadFile(*prepare)
		if err != nil {
			log.Fatal(err)
		}
		paths, err := components.PrepareBundle(*cache, data)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(paths); err != nil {
			log.Fatal(err)
		}
		return
	}
	manifest, err := p2p.Load(*peerAssets)
	if err != nil {
		log.Fatal(err)
	}
	raw := map[string][]byte{}
	for _, platform := range []string{"macos", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "agenterm-client-cli-" + platform + "-" + arch
			if platform == "windows" {
				name += ".exe"
			}
			data, err := os.ReadFile(filepath.Join(*input, name))
			if err != nil {
				log.Fatal(err)
			}
			raw["client-cli/"+name] = data
		}
	}
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(*peerAssets, artifact.File))
		if err != nil {
			log.Fatal(err)
		}
		raw["p2p-cli/"+artifact.File] = data
	}
	if err := writeBundle(*bundle, raw, manifest.Version); err != nil {
		log.Fatal(err)
	}
	files, err := components.BuildClientDownloads(os.DirFS(*input))
	if err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(*output, 0755); err != nil {
		log.Fatal(err)
	}
	for name, data := range files {
		if err = os.WriteFile(filepath.Join(*output, name), data, 0644); err != nil {
			log.Fatal(err)
		}
	}
}

// Publish only complete archives, preserving the previous bundle on failure.
func writeBundle(path string, files map[string][]byte, version string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".components-*.tar.xz")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := components.Pack(f, files, version); err != nil {
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

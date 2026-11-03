// Package p2p contains the shared component bootstrap, not a Go P2P transport.
package p2p

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed bootstrap.sh
var shellTemplate string

//go:embed bootstrap.ps1
var powershellTemplate string

type Artifact struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
}
type Manifest struct {
	Version   string     `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

func Load(directory string) (Manifest, error) {
	var manifest Manifest
	file, err := os.Open(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("p2p-cli assets unavailable: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if !versionPattern.MatchString(manifest.Version) || len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > 6 {
		return manifest, errors.New("invalid p2p-cli manifest")
	}
	seen := map[string]bool{}
	for _, asset := range manifest.Artifacts {
		name, err := Filename(asset.Platform, asset.Arch)
		if err != nil || name != asset.File || seen[name] {
			return manifest, errors.New("invalid or duplicate p2p-cli artifact")
		}
		seen[name] = true
		hash, err := hex.DecodeString(asset.SHA256)
		if err != nil || len(hash) != 32 {
			return manifest, errors.New("invalid p2p-cli checksum")
		}
		// Fail before launching a remote script if the advertised distribution is incomplete.
		f, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return manifest, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(asset.SHA256) {
			return manifest, errors.New("p2p-cli archive checksum mismatch")
		}
	}
	return manifest, nil
}
func Filename(platform, arch string) (string, error) {
	if arch != "amd64" && arch != "arm64" {
		return "", errors.New("unsupported p2p-cli architecture")
	}
	switch platform {
	case "darwin":
		return "p2p-cli-macos-" + arch + ".tar.gz", nil
	case "linux":
		return "p2p-cli-linux-" + arch + ".tar.gz", nil
	case "windows":
		return "p2p-cli-windows-" + arch + ".zip", nil
	default:
		return "", errors.New("unsupported p2p-cli platform")
	}
}
func (m Manifest) Supports(platform, arch string) bool {
	for _, a := range m.Artifacts {
		if a.Platform == platform && a.Arch == arch {
			return true
		}
	}
	return false
}
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Scripts embed only a short-lived local credential. The remote credential is
// supplied through an environment variable by Client and never in its log label.
func Scripts(m Manifest, base string, config []byte, foreground, stop bool) (string, string) {
	var shCases, psCases strings.Builder
	for _, a := range m.Artifacts {
		key := a.Platform + "/" + a.Arch
		url := strings.TrimRight(base, "/") + "/p2p/downloads/" + a.File
		fmt.Fprintf(&shCases, "  %s) url=%s; expected=%s; archive=%s ;;\n", key, shQuote(url), shQuote(strings.ToLower(a.SHA256)), shQuote(a.File))
		fmt.Fprintf(&psCases, "  %s { $url=%s; $expected=%s; $archive=%s }\n", psQuote(key), psQuote(url), psQuote(strings.ToLower(a.SHA256)), psQuote(a.File))
	}
	mode := "background"
	if foreground {
		mode = "foreground"
	}
	if stop {
		mode = "stop"
	}
	var parameters struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(config, &parameters)
	encoded := base64.StdEncoding.EncodeToString(config)
	if stop {
		encoded = ""
	}
	replacements := []string{"@VERSION@", m.Version, "@CONFIG_B64@", encoded, "@MODE@", mode, "@SESSION@", parameters.Session}
	sh := strings.NewReplacer(append(replacements, "@ASSET_CASES@", shCases.String())...).Replace(shellTemplate)
	ps := strings.NewReplacer(append(replacements, "@ASSET_CASES@", psCases.String())...).Replace(powershellTemplate)
	return sh, ps
}

// Package theme adapts the shared theme to the invitation page and installers.
package theme

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

//go:embed theme.json
var themeJSON []byte

var palette = func() configuration {
	var result configuration
	if err := json.Unmarshal(themeJSON, &result); err != nil {
		panic(err)
	}
	return result
}()

type configuration struct {
	TerminalLogo      [3]string `json:"terminal-logo"`
	InstallerWordmark [2]string `json:"installer-wordmark"`
	Web               map[string]string
	Terminal          map[string]string
	Installer         map[string]struct {
		ANSI       string
		PowerShell string
	}
}

// TerminalLogo returns the three rows of the terminal mark.
func TerminalLogo() [3]string {
	return palette.TerminalLogo
}

// InstallerWordmark returns the pixel lettering shared by both installers.
func InstallerWordmark() [2]string {
	return palette.InstallerWordmark
}

// PrimarySGR returns the ANSI truecolor foreground parameters for the installer.
func PrimarySGR() string {
	rgb, err := hex.DecodeString(strings.TrimPrefix(palette.Terminal["primary"], "#"))
	if err != nil || len(rgb) != 3 {
		panic("invalid primary theme color")
	}
	return fmt.Sprintf("38;2;%d;%d;%d", rgb[0], rgb[1], rgb[2])
}

// WebCSS provides the invitation page's CSS color properties.
func WebCSS() string {
	var css strings.Builder
	css.WriteString(":root{color-scheme:light;")
	validRole := regexp.MustCompile(`^[a-z-]+$`)
	validColor := regexp.MustCompile(`^#[a-fA-F0-9]{6}([a-fA-F0-9]{2})?$`)
	for _, role := range slices.Sorted(maps.Keys(palette.Web)) {
		color := palette.Web[role]
		if !validRole.MatchString(role) || !validColor.MatchString(color) {
			panic("invalid theme color: " + role)
		}
		css.WriteString("--" + role + ":" + color + ";")
	}
	css.WriteString("}\n")
	return css.String()
}

// InstallerColor returns the console-native value for a theme role.
func InstallerColor(role string, powershell bool) string {
	color, ok := palette.Installer[role]
	if !ok {
		panic("missing installer color: " + role)
	}
	if powershell {
		return color.PowerShell
	}
	return color.ANSI
}

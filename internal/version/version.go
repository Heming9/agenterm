// Package version defines the application version and connection compatibility.
package version

import (
	"agenterm/internal/i18n"
	"regexp"
	"strings"
)

const Current = "0.10.12"
const Header = "X-Agenterm-Version"

// SemVer 2.0.0 grammar, including the prohibition on leading zeroes in numeric
// prerelease identifiers. A leading v is accepted separately for release tags.
var pattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// Check applies Agenterm's SemVer-based connection policy. Stable versions >=1.0
// share a compatibility line by major version. Since SemVer does not guarantee
// compatibility before 1.0, Agenterm requires matching minor versions for 0.x,
// and matching patch versions for 0.0.x. Prereleases require the same release
// and prerelease identifiers. Build metadata never affects compatibility.
func Check(local, peer string) error {
	l := pattern.FindStringSubmatch(strings.TrimPrefix(local, "v"))
	p := pattern.FindStringSubmatch(strings.TrimPrefix(peer, "v"))
	if l == nil || p == nil {
		return i18n.NewError("version.unknown", local, peer)
	}
	compatible := l[1] == p[1]
	if l[4] != "" || p[4] != "" {
		compatible = compatible && l[2] == p[2] && l[3] == p[3] && l[4] == p[4]
	} else if l[1] == "0" {
		compatible = compatible && l[2] == p[2] && (l[2] != "0" || l[3] == p[3])
	}
	if !compatible {
		return i18n.NewError("version.incompatible", local, peer)
	}
	return nil
}

// Package i18n selects the user's language and holds Agenterm's UI text.
package i18n

import (
	"os"
	"strings"

	locale "github.com/jeandeaual/go-locale"
	"golang.org/x/text/language"
)

// Resolve maps supported Simplified Chinese locales to zh-CN and everything
// else to English. An explicit script takes precedence over the region.
func Resolve(value string) string {
	value = strings.SplitN(strings.SplitN(value, ".", 2)[0], "@", 2)[0]
	value = strings.ReplaceAll(value, "_", "-")
	tag, err := language.Parse(value)
	if err != nil {
		return "en"
	}
	base, _ := tag.Base()
	if base.String() != "zh" {
		return "en"
	}
	script, _ := tag.Script()
	if script.String() == "Hans" {
		return "zh-CN"
	}
	return "en"
}

// Current checks the explicit process override, then the platform locale.
func Current() string {
	if value := os.Getenv("AGENTERM_LANG"); value != "" && value != "auto" {
		return Resolve(value)
	}
	values, err := locale.GetLocales()
	if err != nil || len(values) == 0 {
		return "en"
	}
	return Resolve(values[0])
}

// Text returns the requested translation, using English for all other locales.
func Text(lang, en, zh string) string {
	if Resolve(lang) == "zh-CN" {
		return zh
	}
	return en
}

// Terminal respects the invoking shell's locale before the system setting.
func Terminal() string {
	if value := os.Getenv("AGENTERM_LANG"); value != "" && value != "auto" {
		return Resolve(value)
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			if value != "C" && value != "POSIX" && os.Getenv("LANGUAGE") != "" {
				value = strings.Split(os.Getenv("LANGUAGE"), ":")[0]
			}
			return Resolve(value)
		}
	}
	return Current()
}

// Package i18n selects the user's language and holds Agenterm's UI text.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
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

//go:embed messages.json
var resources embed.FS

var messages = func() map[string]map[string]string {
	data, err := resources.ReadFile("messages.json")
	if err != nil {
		panic(err)
	}
	var result map[string]map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	return result
}()

// Text looks up a stable resource key, falling back to English.
func Text(lang, key string) string {
	entry := messages[key]
	if value := entry[Resolve(lang)]; value != "" {
		return value
	}
	if value := entry["en"]; value != "" {
		return value
	}
	return key
}

// Error retains the resource key so the receiving UI can choose its language.
type Error struct {
	Key  string
	Args []any
}

func (e *Error) Error() string { return e.Localize(Terminal()) }
func (e *Error) Localize(lang string) string {
	args := append([]any(nil), e.Args...)
	for i, arg := range args {
		if err, ok := arg.(*Error); ok {
			args[i] = err.Localize(lang)
		}
	}
	return fmt.Sprintf(strings.ReplaceAll(Text(lang, e.Key), "%w", "%v"), args...)
}
func NewError(key string, args ...any) error { return &Error{Key: key, Args: args} }

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

// Localize renders an application error in the receiving interface's language.
func Localize(err error, lang string) string {
	if e, ok := err.(*Error); ok {
		return e.Localize(lang)
	}
	return err.Error()
}
func (e *Error) Unwrap() error {
	for _, arg := range e.Args {
		if err, ok := arg.(error); ok {
			return err
		}
	}
	return nil
}

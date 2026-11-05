// Package installer contains the installation scripts and release download assets.
package installer

import (
	"agenterm/internal/i18n"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed install.sh install.ps1
var content embed.FS

var resourceKey = regexp.MustCompile(`installer\.[a-z0-9.]+`)

func Script(name string) ([]byte, error) {
	data, err := content.ReadFile(name)
	if err != nil {
		return nil, err
	}
	template := string(data)
	unique := make(map[string]string)
	for _, key := range resourceKey.FindAllString(template, -1) {
		unique[key] = ""
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index, key := range keys {
		unique[key] = fmt.Sprintf("k%d", index)
	}
	template = resourceKey.ReplaceAllStringFunc(template, func(key string) string { return unique[key] })
	return []byte(strings.ReplaceAll(template, "@TRANSLATIONS@", i18n.ScriptTranslations(strings.HasSuffix(name, ".ps1"), unique))), nil
}

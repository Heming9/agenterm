package i18n

import (
	"fmt"
	"sort"
	"strings"
)

// ScriptTranslations renders the requested resources with compact script keys.
// Both languages are included: selection happens on the client.
func ScriptTranslations(powershell bool, names map[string]string) string {
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	if powershell {
		b.WriteString("$texts = @{\n")
	} else {
		b.WriteString("text() {\n case \"$chinese:$1\" in\n")
	}
	for _, key := range keys {
		for index, lang := range []string{"en", "zh-CN"} {
			value := Text(lang, key)
			if powershell {
				// PowerShell uses composite formatting; escape literal braces first.
				value = strings.ReplaceAll(strings.ReplaceAll(value, "{", "{{"), "}", "}}")
				for n := 0; strings.Contains(value, "%s"); n++ {
					value = strings.Replace(value, "%s", fmt.Sprintf("{%d}", n), 1)
				}
				fmt.Fprintf(&b, " '%d:%s' = '%s'\n", index, names[key], strings.ReplaceAll(value, "'", "''"))
			} else {
				fmt.Fprintf(&b, " '%d:%s') printf '%%s' '%s';;\n", index, names[key], strings.ReplaceAll(value, "'", "'\"'\"'"))
			}
		}
	}
	if powershell {
		b.WriteString("}\nfunction Text([string]$Key, [object[]]$Values = @()) { $prefix = if ($chinese) { '1' } else { '0' }; return ($texts[$prefix + ':' + $Key] -f $Values) }\n")
	} else {
		b.WriteString(" esac\n}\n")
	}
	return b.String()
}

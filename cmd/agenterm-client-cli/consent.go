package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// authorizationChoice defaults to temporary in the interactive menu; errors
// and invalid redirected input are denied.
func authorizationChoice(input *bufio.Reader, output io.Writer) int {
	choices := []string{tr("installer.consent.temporary"), tr("installer.consent.persistent"), tr("installer.consent.no")}
	fmt.Fprintln(output)
	fmt.Fprintln(output, tr("installer.consent.menu"))
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 0
		}
		defer term.Restore(int(os.Stdin.Fd()), state)
		selected := 0
		previousLines := 0
		for {
			parts := make([]string, len(choices))
			for i, choice := range choices {
				parts[i] = "   " + choice + " "
			}
			parts[selected] = " ▶ " + choices[selected] + " "
			if os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" {
				parts[selected] = "\x1b[7m" + parts[selected] + "\x1b[0m"
			}
			width, _, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				width = 80
			}
			separator := "    "
			if width < 120 {
				separator = "\r\n"
			}
			if previousLines > 1 {
				fmt.Fprintf(output, "\x1b[%dA", previousLines-1)
			}
			fmt.Fprintf(output, "\r\x1b[J%s", strings.Join(parts, separator))
			previousLines = 1
			if width < 120 {
				previousLines = len(parts)
			}
			b, err := input.ReadByte()
			if err != nil || b == 3 {
				fmt.Fprint(output, "\r\n")
				return 0
			}
			switch b {
			case '\r', '\n':
				fmt.Fprint(output, "\r\n")
				return (selected + 1) % 3
			case 27:
				next, err := input.ReadByte()
				if err != nil || next != '[' {
					return 0
				}
				arrow, err := input.ReadByte()
				if err != nil {
					return 0
				}
				if (arrow == 'C' || arrow == 'B') && selected < 2 {
					selected++
				}
				if (arrow == 'D' || arrow == 'A') && selected > 0 {
					selected--
				}
			}
		}
	}
	fmt.Fprintf(output, "1. %s    2. %s    3. %s\n%s", choices[0], choices[1], choices[2], tr("installer.consent.number"))
	answer, _ := input.ReadString('\n')
	switch strings.TrimSpace(answer) {
	case "1":
		return 1
	case "2":
		return 2
	default:
		return 0
	}
}

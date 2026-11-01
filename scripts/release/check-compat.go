// Release planning must use the same compatibility policy as connections.
package main

import (
	"agenterm/internal/version"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: check-compat VERSION VERSION")
		os.Exit(2)
	}
	if err := version.Check(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

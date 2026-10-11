// SPDX-License-Identifier: GPL-3.0-or-later

// Command meta runs the Panelarr metadata service.
package main

import (
	"fmt"
	"os"

	"github.com/panelarr/meta/internal/buildinfo"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(buildinfo.Version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: meta version")
	os.Exit(2)
}

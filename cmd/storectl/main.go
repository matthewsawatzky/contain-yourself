// storectl validates app-store submissions and generates the compact index.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"workstation-manager/internal/appstore"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "build" && os.Args[1] != "check") {
		fmt.Fprintln(os.Stderr, "usage: storectl build|check [app-store-directory]")
		os.Exit(2)
	}
	root := "app_store"
	if len(os.Args) > 2 {
		root = os.Args[2]
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		fatal(err)
	}
	switch os.Args[1] {
	case "build":
		index, err := appstore.Build(absolute, true)
		if err != nil {
			fatal(err)
		}
		if err := appstore.WriteIndex(absolute, index); err != nil {
			fatal(err)
		}
		fmt.Printf("Built %s with %d apps\n", filepath.Join(absolute, "index.json"), len(index.Apps))
	case "check":
		if err := appstore.CheckIndex(absolute); err != nil {
			fatal(err)
		}
		fmt.Printf("App store is valid (%s)\n", absolute)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "storectl:", err)
	os.Exit(1)
}

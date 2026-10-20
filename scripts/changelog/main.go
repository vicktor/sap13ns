// Command changelog prints the release notes of a version from
// CHANGELOG.md: its section and the older ones (internal/update.Notes). The
// release workflow publishes them; it fails when the version has no section.
//
//	go run ./scripts/changelog -version v1.4.0 [CHANGELOG.md]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"sap13ns/internal/update"
)

func main() {
	version := flag.String("version", "", "release tag, such as v1.4.0")
	flag.Parse()
	file := "CHANGELOG.md"
	if flag.NArg() > 0 {
		file = flag.Arg(0)
	}
	if *version == "" {
		log.Fatal("changelog: -version is required")
	}
	md, err := os.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	notes, err := update.Notes(string(md), *version)
	if err != nil {
		log.Fatalf("changelog: %v", err)
	}
	fmt.Print(notes)
}

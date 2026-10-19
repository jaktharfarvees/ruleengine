// Command schemacompat verifies that published JSON schemas evolved only by
// adding optional properties or reusable definitions.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/arrowhr/ruleengine/internal/schemacompat"
)

func main() {
	baseline := flag.String("baseline", "schema/compat/v2", "directory containing immutable published schema snapshots")
	current := flag.String("current", "schema/v2", "directory containing the schemas to verify")
	flag.Parse()

	violations, err := schemacompat.CheckSnapshots(*baseline, *current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schema compatibility check failed: %v\n", err)
		os.Exit(2)
	}
	if len(violations) != 0 {
		fmt.Fprintln(os.Stderr, "incompatible schema changes:")
		for _, violation := range violations {
			fmt.Fprintf(os.Stderr, "- %s\n", violation.Error())
		}
		os.Exit(1)
	}
	fmt.Println("schema compatibility: additive-only contract preserved")
}

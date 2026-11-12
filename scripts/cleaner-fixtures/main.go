// Command cleaner-fixtures prints one fixture's cleaned text as base64, so a
// shell parity check can compare it with the Python implementation without
// losing a trailing newline to command substitution.
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/aryadhira/iris/internal/reply"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cleaner-fixtures <file>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(base64.StdEncoding.EncodeToString([]byte(reply.Clean(string(raw)))))
}

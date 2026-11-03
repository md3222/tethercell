// Command usbtether-host is the bare host (no supervisor); see internal/host.
// Everyday use is cmd/usbtether, which wraps this with reconnects and phone setup.
package main

import (
	"os"

	"tetrd-clone/internal/host"
)

func main() { host.Main(os.Args[1:]) }

//go:build windows

// Command tethercell-host is the bare host (no supervisor); see internal/host.
package main

import (
	"os"

	"tethercell/internal/host"
)

func main() { host.Main(os.Args[1:]) }

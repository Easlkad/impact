package main

import (
	"fmt"
	"io"
	"runtime/debug"
)

// version is the version of impact. Release builds set it at link time:
//
//	go build -ldflags "-X main.version=v1.0.0" ./cmd/impact
var version = "dev"

// toolVersion returns the version set at link time or, for a binary built
// with "go install module@version", the module version; otherwise "dev".
func toolVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func runVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "impact %s\n", toolVersion())
	return exitOK
}

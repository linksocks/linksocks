package wssocks

import "runtime"

var (
	Version = "v1.0.1"
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

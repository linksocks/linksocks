package wssocks

import "runtime"

var (
	Version = "v1.1.0"
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

package wssocks

import "runtime"

var (
	Version  = "v1.0.0"
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

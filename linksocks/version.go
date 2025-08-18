package linksocks

import "runtime"

var (
	Version  = "v3.0.0"
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

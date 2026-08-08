package linksocks

import "runtime"

var (
	Version  = "v2.0.0"
	Platform = runtime.GOOS + "/" + runtime.GOARCH
)

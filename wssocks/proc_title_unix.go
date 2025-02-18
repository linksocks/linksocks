//go:build !windows

package wssocks

import "github.com/erikdubbelboer/gspt"

func setProcessTitle(title string) {
	gspt.SetProcTitle(title)
}

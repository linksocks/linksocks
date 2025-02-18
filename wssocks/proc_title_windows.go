//go:build windows

package wssocks

import "github.com/rs/zerolog/log"

func setProcessTitle(title string) {
	log.Warn().Msg("Process title setting is not supported on Windows")
}

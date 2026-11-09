package linksocks

import (
	"github.com/linksocks/linksocks/internal/ech"
)

// ECH modes accepted by --ech and LINKSOCKS_ECH.
const (
	// ECHAuto looks up the ECH config list published in the relay host's
	// HTTPS DNS record and offers Encrypted Client Hello when one is found.
	ECHAuto = ech.Auto
	// ECHOff connects with a plain TLS ClientHello.
	ECHOff = ech.Off
)

// ParseECHMode validates an ECH mode value.
func ParseECHMode(value string) (string, error) {
	return ech.ParseMode(value)
}

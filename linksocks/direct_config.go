package linksocks

import (
	"fmt"
	"strings"
)

type DirectMode string

type DirectDiscovery string

type DirectHostCandidatesMode string

type DirectOnlyAction string

const (
	DirectModeRelayOnly  DirectMode = "relay-only"
	DirectModeDirectOnly DirectMode = "direct-only"
	DirectModeAuto       DirectMode = "auto"
)

const (
	DirectDiscoverySTUN   DirectDiscovery = "stun"
	DirectDiscoveryServer DirectDiscovery = "server"
	DirectDiscoveryAuto   DirectDiscovery = "auto"
)

const (
	DirectOnlyActionExit   DirectOnlyAction = "exit"
	DirectOnlyActionRefuse DirectOnlyAction = "refuse"
)

const (
	DirectHostCandidatesAuto   DirectHostCandidatesMode = "auto"
	DirectHostCandidatesNever  DirectHostCandidatesMode = "never"
	DirectHostCandidatesAlways DirectHostCandidatesMode = "always"
)

func ParseDirectMode(v string) (DirectMode, error) {
	m := DirectMode(strings.TrimSpace(strings.ToLower(v)))
	switch m {
	case DirectModeRelayOnly, DirectModeDirectOnly, DirectModeAuto:
		return m, nil
	default:
		return "", fmt.Errorf("invalid direct-mode: %q (supported: %s|%s|%s)", v, DirectModeRelayOnly, DirectModeDirectOnly, DirectModeAuto)
	}
}

func ParseDirectDiscovery(v string) (DirectDiscovery, error) {
	d := DirectDiscovery(strings.TrimSpace(strings.ToLower(v)))
	switch d {
	case DirectDiscoverySTUN, DirectDiscoveryServer, DirectDiscoveryAuto:
		return d, nil
	default:
		return "", fmt.Errorf("invalid direct-discovery: %q (supported: %s|%s|%s)", v, DirectDiscoverySTUN, DirectDiscoveryServer, DirectDiscoveryAuto)
	}
}

func ParseDirectOnlyAction(v string) (DirectOnlyAction, error) {
	a := DirectOnlyAction(strings.TrimSpace(strings.ToLower(v)))
	switch a {
	case DirectOnlyActionExit, DirectOnlyActionRefuse:
		return a, nil
	default:
		return "", fmt.Errorf("invalid direct-only-action: %q (supported: %s|%s)", v, DirectOnlyActionExit, DirectOnlyActionRefuse)
	}
}

func ParseDirectHostCandidatesMode(v string) (DirectHostCandidatesMode, error) {
	m := DirectHostCandidatesMode(strings.TrimSpace(strings.ToLower(v)))
	switch m {
	case DirectHostCandidatesAuto, DirectHostCandidatesNever, DirectHostCandidatesAlways:
		return m, nil
	default:
		return "", fmt.Errorf("invalid direct-host-candidates: %q (supported: %s|%s|%s)", v, DirectHostCandidatesAuto, DirectHostCandidatesNever, DirectHostCandidatesAlways)
	}
}

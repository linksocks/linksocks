package tests

import (
	"net"
	"reflect"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"

	_ "unsafe"
)

//go:linkname formatBatchProgressSuffix github.com/linksocks/linksocks/linksocks.formatBatchProgressSuffix
func formatBatchProgressSuffix(count, total int) string

//go:linkname directSelectQUICDialCandidates github.com/linksocks/linksocks/linksocks.directSelectQUICDialCandidates
func directSelectQUICDialCandidates(localSession uuid.UUID, pairSession uuid.UUID, probePeer *net.UDPAddr, rcands []linksocks.DirectCandidate) []linksocks.DirectCandidate

func callChannelMethod(ch reflect.Value, name string, args ...reflect.Value) []reflect.Value {
	return ch.MethodByName(name).Call(args)
}
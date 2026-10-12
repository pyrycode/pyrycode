//go:build !permission_observe

package relay

import "github.com/pyrycode/pyrycode/internal/protocol"

type permissionObserver struct{}
type permissionQueueObserver struct{}
type permissionObservation struct{}

func (m *V2SessionManager) newPermissionQueue() permissionQueueObserver {
	return permissionQueueObserver{}
}
func (m *V2SessionManager) permissionAttempt(*pushQueue, protocol.Envelope, *LiveState) permissionObservation {
	return permissionObservation{}
}
func (permissionObservation) record(string)                                                   {}
func (*pushQueue) observePermissions(string)                                                  {}
func (*V2SessionManager) observePermissionCancellation(string, protocol.Envelope, *LiveState) {}
func (*V2SessionManager) observePermissionTransportHold()                                     {}
func (*V2SessionManager) observePermissionHold(string, string)                                {}

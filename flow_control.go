package webtransport

import (
	"errors"
	"fmt"
	"sync"
)

var errMaxDataNotIncreased = errors.New("webtransport: WT_MAX_DATA capsule didn't increase data limit")

type outgoingDataFlowController struct {
	mx sync.Mutex
	// TODO: Update this and notify waiters when WT_MAX_DATA is implemented.
	maxData       int64
	bytesSent     int64
	lastBlockedAt int64
	updated       chan struct{}
}

func newOutgoingDataFlowController(maxData int64) *outgoingDataFlowController {
	return &outgoingDataFlowController{
		maxData:       maxData,
		lastBlockedAt: -1,
		updated:       make(chan struct{}),
	}
}

func (f *outgoingDataFlowController) AddBytesSent(n int64) int64 {
	f.mx.Lock()
	defer f.mx.Unlock()

	var added int64
	if f.bytesSent < f.maxData {
		added = min(n, f.maxData-f.bytesSent)
	}
	if added > 0 {
		f.bytesSent += added
	}
	return added
}

func (f *outgoingDataFlowController) IsNewlyBlocked() (bool, int64) {
	f.mx.Lock()
	defer f.mx.Unlock()

	if f.bytesSent < f.maxData || f.maxData == f.lastBlockedAt {
		return false, 0
	}
	f.lastBlockedAt = f.maxData
	return true, f.maxData
}

func (f *outgoingDataFlowController) UpdateMaxData(n uint64) error {
	f.mx.Lock()
	defer f.mx.Unlock()

	maxData := int64(n)
	if maxData <= f.maxData {
		return fmt.Errorf("%w: current limit: %d, received limit: %d", errMaxDataNotIncreased, f.maxData, maxData)
	}
	f.maxData = maxData
	close(f.updated)
	f.updated = make(chan struct{})
	return nil
}

func (f *outgoingDataFlowController) NextUpdate() <-chan struct{} {
	f.mx.Lock()
	updated := f.updated
	f.mx.Unlock()
	return updated
}

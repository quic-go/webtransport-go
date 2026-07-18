package webtransport

import (
	"errors"
	"fmt"
	"sync"

	"github.com/quic-go/quic-go/quicvarint"
)

var errMaxDataNotIncreased = errors.New("webtransport: WT_MAX_DATA capsule didn't increase data limit")

type outgoingDataFlowController struct {
	mx sync.Mutex
	// TODO: Update this and notify waiters when WT_MAX_DATA is implemented.
	maxData       uint64
	bytesSent     uint64
	lastBlockedAt uint64
	updated       chan struct{}
}

func newOutgoingDataFlowController(maxData uint64) *outgoingDataFlowController {
	return &outgoingDataFlowController{
		maxData: maxData,
		updated: make(chan struct{}),
	}
}

func (f *outgoingDataFlowController) AddBytesSent(n uint64) uint64 {
	f.mx.Lock()
	defer f.mx.Unlock()

	var added uint64
	if f.bytesSent < f.maxData {
		added = min(n, f.maxData-f.bytesSent)
	}
	if added > 0 {
		f.bytesSent += added
	}
	return added
}

func (f *outgoingDataFlowController) IsNewlyBlocked() (bool, uint64) {
	f.mx.Lock()
	defer f.mx.Unlock()

	if f.bytesSent < f.maxData || f.maxData == f.lastBlockedAt {
		return false, 0
	}
	f.lastBlockedAt = f.maxData
	return true, f.maxData
}

func (f *outgoingDataFlowController) UpdateMaxData(maxData uint64) error {
	f.mx.Lock()
	defer f.mx.Unlock()

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

type incomingDataFlowController struct {
	mx sync.Mutex

	bytesRead         uint64
	maxData           uint64
	receiveWindow     uint64
	queueWindowUpdate func(uint64)
}

func newIncomingDataFlowController(bytesRead, maxData uint64, queueWindowUpdate func(uint64)) *incomingDataFlowController {
	return &incomingDataFlowController{
		bytesRead:         bytesRead,
		maxData:           maxData,
		receiveWindow:     maxData - bytesRead,
		queueWindowUpdate: queueWindowUpdate,
	}
}

func (f *incomingDataFlowController) AddBytesRead(n uint64) error {
	f.mx.Lock()
	defer f.mx.Unlock()

	if n > f.maxData-f.bytesRead {
		return fmt.Errorf("webtransport: received more than %d bytes of stream data", f.maxData)
	}
	f.bytesRead += n
	if f.maxData-f.bytesRead > 3*f.receiveWindow/4 {
		return nil
	}

	maxData := min(f.bytesRead+f.receiveWindow, uint64(quicvarint.Max))
	if maxData == f.maxData {
		return nil
	}
	f.maxData = maxData
	f.queueWindowUpdate(maxData)
	return nil
}

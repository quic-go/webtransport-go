// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/quic-go/quic-go/http3 (interfaces: StreamCreator)
//
// Generated by this command:
//
//	mockgen -package webtransport -destination mock_stream_creator_test.go github.com/quic-go/quic-go/http3 StreamCreator
//
// Package webtransport is a generated GoMock package.
package webtransport

import (
	context "context"
	net "net"
	reflect "reflect"

	quic "github.com/quic-go/quic-go"
	gomock "go.uber.org/mock/gomock"
)

// MockStreamCreator is a mock of StreamCreator interface.
type MockStreamCreator struct {
	ctrl     *gomock.Controller
	recorder *MockStreamCreatorMockRecorder
}

// MockStreamCreatorMockRecorder is the mock recorder for MockStreamCreator.
type MockStreamCreatorMockRecorder struct {
	mock *MockStreamCreator
}

// NewMockStreamCreator creates a new mock instance.
func NewMockStreamCreator(ctrl *gomock.Controller) *MockStreamCreator {
	mock := &MockStreamCreator{ctrl: ctrl}
	mock.recorder = &MockStreamCreatorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockStreamCreator) EXPECT() *MockStreamCreatorMockRecorder {
	return m.recorder
}

// ConnectionState mocks base method.
func (m *MockStreamCreator) ConnectionState() quic.ConnectionState {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ConnectionState")
	ret0, _ := ret[0].(quic.ConnectionState)
	return ret0
}

// ConnectionState indicates an expected call of ConnectionState.
func (mr *MockStreamCreatorMockRecorder) ConnectionState() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ConnectionState", reflect.TypeOf((*MockStreamCreator)(nil).ConnectionState))
}

// Context mocks base method.
func (m *MockStreamCreator) Context() context.Context {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Context")
	ret0, _ := ret[0].(context.Context)
	return ret0
}

// Context indicates an expected call of Context.
func (mr *MockStreamCreatorMockRecorder) Context() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Context", reflect.TypeOf((*MockStreamCreator)(nil).Context))
}

// LocalAddr mocks base method.
func (m *MockStreamCreator) LocalAddr() net.Addr {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LocalAddr")
	ret0, _ := ret[0].(net.Addr)
	return ret0
}

// LocalAddr indicates an expected call of LocalAddr.
func (mr *MockStreamCreatorMockRecorder) LocalAddr() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LocalAddr", reflect.TypeOf((*MockStreamCreator)(nil).LocalAddr))
}

// OpenStream mocks base method.
func (m *MockStreamCreator) OpenStream() (quic.Stream, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "OpenStream")
	ret0, _ := ret[0].(quic.Stream)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// OpenStream indicates an expected call of OpenStream.
func (mr *MockStreamCreatorMockRecorder) OpenStream() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OpenStream", reflect.TypeOf((*MockStreamCreator)(nil).OpenStream))
}

// OpenStreamSync mocks base method.
func (m *MockStreamCreator) OpenStreamSync(arg0 context.Context) (quic.Stream, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "OpenStreamSync", arg0)
	ret0, _ := ret[0].(quic.Stream)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// OpenStreamSync indicates an expected call of OpenStreamSync.
func (mr *MockStreamCreatorMockRecorder) OpenStreamSync(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OpenStreamSync", reflect.TypeOf((*MockStreamCreator)(nil).OpenStreamSync), arg0)
}

// OpenUniStream mocks base method.
func (m *MockStreamCreator) OpenUniStream() (quic.SendStream, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "OpenUniStream")
	ret0, _ := ret[0].(quic.SendStream)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// OpenUniStream indicates an expected call of OpenUniStream.
func (mr *MockStreamCreatorMockRecorder) OpenUniStream() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OpenUniStream", reflect.TypeOf((*MockStreamCreator)(nil).OpenUniStream))
}

// OpenUniStreamSync mocks base method.
func (m *MockStreamCreator) OpenUniStreamSync(arg0 context.Context) (quic.SendStream, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "OpenUniStreamSync", arg0)
	ret0, _ := ret[0].(quic.SendStream)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// OpenUniStreamSync indicates an expected call of OpenUniStreamSync.
func (mr *MockStreamCreatorMockRecorder) OpenUniStreamSync(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OpenUniStreamSync", reflect.TypeOf((*MockStreamCreator)(nil).OpenUniStreamSync), arg0)
}

// RemoteAddr mocks base method.
func (m *MockStreamCreator) RemoteAddr() net.Addr {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoteAddr")
	ret0, _ := ret[0].(net.Addr)
	return ret0
}

// RemoteAddr indicates an expected call of RemoteAddr.
func (mr *MockStreamCreatorMockRecorder) RemoteAddr() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoteAddr", reflect.TypeOf((*MockStreamCreator)(nil).RemoteAddr))
}

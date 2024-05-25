package net

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCon struct {
	remoteAddr net.Addr
}

func (f *fakeCon) Read(_ []byte) (n int, err error) {
	return 0, nil
}

func (f *fakeCon) Write(_ []byte) (n int, err error) {
	return 0, nil
}

func (f *fakeCon) Close() error {
	return nil
}

func (f *fakeCon) LocalAddr() net.Addr {
	return nil
}

func (f *fakeCon) RemoteAddr() net.Addr {
	return f.remoteAddr
}

func (f *fakeCon) SetDeadline(_ time.Time) error {
	return nil
}

func (f *fakeCon) SetReadDeadline(_ time.Time) error {
	return nil
}

func (f *fakeCon) SetWriteDeadline(_ time.Time) error {
	return nil
}

var _ net.Conn = &fakeCon{}

type fakeListener struct {
	addr         net.Addr
	index        int
	err          error
	closed       atomic.Bool
	connErrPairs []connErrPair
}

func (f *fakeListener) Accept() (net.Conn, error) {
	if f.index < len(f.connErrPairs) {
		index := f.index
		connErr := f.connErrPairs[index]
		f.index++
		return connErr.conn, connErr.err
	}
	for {
		if f.closed.Load() {
			return nil, fmt.Errorf("use of closed network connection")
		}
	}
}

func (f *fakeListener) Close() error {
	f.closed.Store(true)
	return nil
}

func (f *fakeListener) Addr() net.Addr {
	return f.addr
}

var _ net.Listener = &fakeListener{}

func TestMultiListen(t *testing.T) {
	resolveTCPAddrFunc := func(_, address string) (*net.TCPAddr, error) {
		host, portInt, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portInt)
		if err != nil {
			return nil, err
		}
		return &net.TCPAddr{
			IP:   ParseIPSloppy(host),
			Port: port,
		}, nil
	}

	listenFuncFactory := func(listeners []*fakeListener) func(_ context.Context, network string, address string) (net.Listener, error) {
		index := 0
		return func(_ context.Context, network string, address string) (net.Listener, error) {
			if index < len(listeners) {
				listener := listeners[index]
				addr, err := resolveTCPAddrFunc(network, address)
				if err != nil {
					return nil, err
				}
				listener.addr = addr
				index++

				if listener.err != nil {
					return nil, listener.err
				}
				return listener, nil
			}
			return nil, nil
		}
	}

	testCases := []struct {
		name                 string
		addresses            []string
		acceptCalls          int
		fakeListeners        []*fakeListener
		expectMultiListenErr bool
	}{
		{
			name:        "close should close all the listeners",
			addresses:   []string{"127.0.0.1:3000", "[::1]:4000", "192.168.1.10:5000", "[fd00:192:168:59::103]:6000"},
			acceptCalls: 6,
			fakeListeners: []*fakeListener{
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("127.0.0.1"), Port: 30001}},
							err:  nil,
						},
					},
				},
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("::1"), Port: 30002}},
							err:  nil,
						},
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("::1"), Port: 30003}},
							err:  nil,
						},
					},
				},
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("192.168.1.10"), Port: 30004}},
							err:  nil,
						},
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("192.168.1.10"), Port: 30005}},
							err:  nil,
						},
					},
				},
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("fd00:192:168:59::103"), Port: 30006}},
							err:  nil,
						},
					},
				},
			},
			expectMultiListenErr: false,
		},
		{
			name:        "fail to resolve one address should close all other listeners",
			addresses:   []string{"127.0.0.1:3000", "[:::1]:3000", "192.168.1.10:4000", "[fd00:192:168:59::103]:4000"},
			acceptCalls: 0,
			fakeListeners: []*fakeListener{
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("127.0.0.1"), Port: 30001}},
							err:  nil,
						},
					},
				},
			},
			expectMultiListenErr: true,
		},
		{
			name:        "fail to listen on one address should close all other listeners",
			addresses:   []string{"127.0.0.1:3000", "[::1]:3000", "192.168.1.10:4000", "[fd00:192:168:59::103]:4000"},
			acceptCalls: 0,
			fakeListeners: []*fakeListener{
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("127.0.0.1"), Port: 30001}},
							err:  nil,
						},
					},
				},
				{
					connErrPairs: []connErrPair{
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("::1"), Port: 30002}},
							err:  nil,
						},
						{
							conn: &fakeCon{remoteAddr: &net.TCPAddr{IP: ParseIPSloppy("::1"), Port: 30003}},
							err:  nil,
						},
					},
				},
				{
					err: fmt.Errorf("failed to listen"),
				},
			},
			expectMultiListenErr: true,
		},
		{
			name:                 "should return error in case of no address",
			addresses:            []string{},
			acceptCalls:          0,
			fakeListeners:        []*fakeListener{},
			expectMultiListenErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.TODO()
			ml, err := multiListen(ctx, tc.addresses, resolveTCPAddrFunc, listenFuncFactory(tc.fakeListeners))
			if tc.expectMultiListenErr && err == nil {
				t.Errorf("Expected error but received none")
			} else if !tc.expectMultiListenErr && err != nil {
				t.Errorf("Expected no err but received '%s'", err.Error())
			}

			if tc.expectMultiListenErr {
				// all the listeners should be closed when failed to create multiListener instance
				for i := range tc.fakeListeners {
					if tc.fakeListeners[i].err == nil && !tc.fakeListeners[i].closed.Load() {
						t.Errorf("Expected listener '%s' to be closed", tc.fakeListeners[i].Addr())
					}
				}
			} else {
				for i := 0; i < tc.acceptCalls; i++ {
					_, err := ml.Accept()
					if err != nil {
						t.Errorf("Expected no err but received '%s'", err.Error())
					}

					// ml.Addr() should always return addr of the first listener.
					if ml.Addr().String() != tc.fakeListeners[0].Addr().String() {
						t.Errorf("Expected '%s', got '%s'", ml.Addr(), tc.fakeListeners[0].Addr())
					}
				}

				err = ml.Close()
				if err != nil {
					t.Errorf("Expected no err but received '%s'", err.Error())
				}

				// ml.Close() should close all the listeners
				for i := range tc.fakeListeners {
					if !tc.fakeListeners[i].closed.Load() {
						t.Errorf("Expected listener '%s' to be closed", tc.fakeListeners[i].Addr())
					}
				}

				// calling Accept() after Close() should return error without blocking
				_, err = ml.Accept()
				if err == nil {
					t.Errorf("Expected error but received none")
				}
			}
		})
	}
}

/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package net

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// connErrPair pairs conn and error returned by accept on actual listeners.
// It is used for communication between main and listener goroutines.
type connErrPair struct {
	conn net.Conn
	err  error
}

// multiListener implements net.Listener
type multiListener struct {
	addrs     []net.Addr
	listeners []net.Listener

	wg        sync.WaitGroup
	mu        sync.Mutex
	connErrCh chan connErrPair
	closed    bool
}

// compile time check to ensure *multiListener implements net.Listener.
var _ net.Listener = &multiListener{}

// MultiListen returns net.Listener which can listen for and accept
// TCP connections on multiple addresses.
func MultiListen(ctx context.Context, addresses []string) (net.Listener, error) {
	return multiListen(
		ctx,
		addresses,
		net.ResolveTCPAddr,
		func(ctx context.Context, network, address string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, address)
		})
}

// multiListen consumes stdlib dependencies as argument allowing mocking
// for unit-tests.
func multiListen(
	ctx context.Context,
	addresses []string,
	resolveTCPAddrFunc func(network, address string) (*net.TCPAddr, error),
	listenFunc func(ctx context.Context, network, address string) (net.Listener, error),
) (net.Listener, error) {
	ml := &multiListener{
		connErrCh: make(chan connErrPair),
	}

	if len(addresses) == 0 {
		return nil, fmt.Errorf("no address provided to listen on")
	}
	for _, address := range addresses {
		addr, err := resolveTCPAddrFunc("tcp", address)
		if err != nil {
			// close all listeners
			_ = ml.Close()
			return nil, err
		}

		var network string
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			// close all listeners
			_ = ml.Close()
			return nil, err
		}
		switch IPFamilyOf(ParseIPSloppy(host)) {
		case IPv4:
			network = "tcp4"
		case IPv6:
			network = "tcp6"
		default:
			// close all listeners
			_ = ml.Close()
			return nil, fmt.Errorf("failed to identify ip family of address '%s", addr.String())
		}

		l, err := listenFunc(ctx, network, addr.String())
		if err != nil {
			// close all listeners
			_ = ml.Close()
			return nil, err
		}

		ml.addrs = append(ml.addrs, addr)
		ml.listeners = append(ml.listeners, l)
	}

	for _, l := range ml.listeners {
		ml.wg.Add(1)
		// spawn a go routine for every listener to wait for incoming connection requests
		go func(l net.Listener) {
			defer ml.wg.Done()
			for {
				conn, err := l.Accept()
				ml.mu.Lock()
				if ml.closed {
					ml.mu.Unlock()
					return
				}
				ml.connErrCh <- connErrPair{conn: conn, err: err}
				ml.mu.Unlock()
			}
		}(l)
	}
	return ml, nil
}

// Accept is part of net.Listener interface.
func (ml *multiListener) Accept() (net.Conn, error) {
	connErr, ok := <-ml.connErrCh
	if !ok {
		return nil, fmt.Errorf("use of closed network connection")
	}
	return connErr.conn, connErr.err

}

// Close is part of net.Listener interface.
func (ml *multiListener) Close() error {
	ml.mu.Lock()
	ml.closed = true
	close(ml.connErrCh)
	ml.mu.Unlock()
	for _, l := range ml.listeners {
		_ = l.Close()
	}
	ml.wg.Wait()
	return nil
}

// Addr is part of net.Listener interface.
// Addr always returns the address of the first listener, callers should
// rely on conn.LocalAddr() to get the actual local address of the listener.
func (ml *multiListener) Addr() net.Addr {
	return ml.addrs[0]
}

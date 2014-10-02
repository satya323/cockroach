// Copyright 2014 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License. See the AUTHORS file
// for names of contributors.
//
// Author: Spencer Kimball (spencer.kimball@gmail.com)

package rpc

import (
	"net"
	"net/rpc"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/util"
	"github.com/cockroachdb/cockroach/util/hlc"
)

func init() {
	// Setting the heartbeat interval in individual tests
	// triggers the race detector, so this is better for now.
	heartbeatInterval = 5 * time.Millisecond
}

func TestClientHeartbeat(t *testing.T) {
	tlsConfig, err := LoadTestTLSConfig("..")
	if err != nil {
		t.Fatal(err)
	}

	clock := hlc.NewClock(hlc.UnixNano)
	addr := util.CreateTestAddr("tcp")
	s := NewServer(addr, tlsConfig, clock)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	c := NewClient(s.Addr(), nil, tlsConfig, clock)
	<-c.Ready
	if c != NewClient(s.Addr(), nil, tlsConfig, clock) {
		t.Fatal("expected cached client to be returned while healthy")
	}
	<-c.Ready
	s.Close()
}

// TestClientHeartbeatBadServer verifies that the client is not marked
// as "ready" until a heartbeat request succeeds.
func TestClientHeartbeatBadServer(t *testing.T) {
	tlsConfig, err := LoadTestTLSConfig("..")
	if err != nil {
		t.Fatal(err)
	}

	addr := util.CreateTestAddr("tcp")
	// Create a server which doesn't support heartbeats.
	s := &Server{
		Server:         rpc.NewServer(),
		tlsConfig:      tlsConfig,
		addr:           addr,
		closeCallbacks: make([]func(conn net.Conn), 0, 1),
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	// Now, create a client. It should attempt a heartbeat and fail,
	// causing retry loop to activate.
	clock := hlc.NewClock(hlc.UnixNano)
	c := NewClient(s.Addr(), nil, tlsConfig, clock)
	select {
	case <-c.Ready:
		t.Error("unexpected client heartbeat success")
	case <-c.Closed:
	}
	s.Close()
}

func TestOffsetMeasurement(t *testing.T) {
	tlsConfig, err := LoadTestTLSConfig("..")
	if err != nil {
		t.Fatal(err)
	}

	// Create the server so that we can register a manual clock.
	addr := util.CreateTestAddr("tcp")
	s := &Server{
		Server:    rpc.NewServer(),
		tlsConfig: tlsConfig,
		addr:      addr,
	}
	serverManual := hlc.ManualClock(10)
	serverClock := hlc.NewClock(serverManual.UnixNano)
	heartbeat := &HeartbeatService{
		clock: serverClock,
	}
	s.RegisterName("Heartbeat", heartbeat)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	// Create a client that is 10 nanoseconds behind the server.
	advancing := AdvancingClock{time: 0, advancementInterval: 10}
	clientClock := hlc.NewClock(advancing.UnixNano)
	c := NewClient(s.Addr(), nil, tlsConfig, clientClock)
	<-c.Ready
	expectedOffset := Offset{Offset: 5, Delay: 10}
	if c.Offset().Offset != expectedOffset.Offset ||
		c.Offset().Delay != expectedOffset.Delay {
		t.Errorf("expected offset %v, actual %v",
			expectedOffset, c.Offset())
	}
}

func TestFailedOffestMeasurement(t *testing.T) {
	tlsConfig, err := LoadTestTLSConfig("..")
	if err != nil {
		t.Fatal(err)
	}

	// Create the server so that we can register the heartbeat manually.
	addr := util.CreateTestAddr("tcp")
	s := &Server{
		Server:    rpc.NewServer(),
		tlsConfig: tlsConfig,
		addr:      addr,
	}
	serverManual := hlc.ManualClock(10)
	serverClock := hlc.NewClock(serverManual.UnixNano)
	heartbeat := &ManualHeartbeatService{
		clock: serverClock,
		ready: make(chan bool),
	}
	s.RegisterName("Heartbeat", heartbeat)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	// Create a client that never receives a heartbeat after the first.
	clientManual := hlc.ManualClock(0)
	clientClock := hlc.NewClock(clientManual.UnixNano)
	c := NewClient(s.Addr(), nil, tlsConfig, clientClock)
	heartbeat.ready <- true // Allow one heartbeat for initialization.
	<-c.Ready
	// Wait until there must have been a missed heartbeat.
	time.Sleep(heartbeatInterval * 4)
	if c.Offset() != InfiniteOffset {
		t.Errorf("expected offset %v, actual %v",
			InfiniteOffset, c.Offset())
	}
	s.Close()
}

type AdvancingClock struct {
	time                int64
	advancementInterval int64
}

func (ac *AdvancingClock) UnixNano() int64 {
	time := ac.time
	ac.time = time + ac.advancementInterval
	return time
}

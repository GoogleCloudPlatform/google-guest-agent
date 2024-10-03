//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package mocking contains mocking data structures and serves only for testing
// purposes.
package mocking

// TestAddr is a test implementation of net.Addr. It's exposed so other tests
// can use it overriding DefaultInterfaceOps to increase coverage where we need
// data from the underlying OS.
type TestAddr struct {
	// Addr is the address.
	Addr string
	// NetworkName is the network.
	NetworkName string
}

// String returns the address.
func (a TestAddr) String() string {
	return a.Addr
}

// Network returns the network.
func (a TestAddr) Network() string {
	return a.NetworkName
}

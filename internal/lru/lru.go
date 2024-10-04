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

// Package lru implements a simple LRU cache.
package lru

import (
	"container/list"
	"sync"
)

// dataNode is a node in the LRU cache.
type dataNode struct {
	Data   any
	KeyPtr *list.Element
}

// Handle is the actual LRU cache.
type Handle[K comparable] struct {
	// mu protects the map and the ring.
	mu sync.Mutex
	// lookupTable is the map of key to node.
	lookupTable map[K]*dataNode
	// ring is the ring of elements in the cache.
	ring *list.List
	// capacity is the maximum number of elements in the cache.
	capacity uint
}

// New creates a new LRU cache.
func New[K comparable](capacity uint) *Handle[K] {
	return &Handle[K]{lookupTable: make(map[K]*dataNode), ring: list.New(), capacity: capacity}
}

// Get returns the value associated with the key and a boolean indicating if the
// key was found. If found the node is promoted to the front.
func (l *Handle[K]) Get(key K) (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if node, ok := l.lookupTable[key]; ok {
		l.ring.MoveToFront(node.KeyPtr)
		return node.Data, true
	}

	var defaultVal any
	return defaultVal, false
}

// Put puts the value associated with the key.
func (l *Handle[K]) Put(key K, value any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if node, found := l.lookupTable[key]; found {
		node.Data = value
		l.lookupTable[key] = node
		l.ring.MoveToFront(node.KeyPtr)
	} else {
		if l.capacity == uint(len(l.lookupTable)) {
			back := l.ring.Back()
			l.ring.Remove(back)
			delete(l.lookupTable, back.Value.(K))
		}
		l.lookupTable[key] = &dataNode{Data: value, KeyPtr: l.ring.PushFront(key)}
	}
}

// Len returns the number of elements in the cache.
func (l *Handle[K]) Len() uint {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint(len(l.lookupTable))
}

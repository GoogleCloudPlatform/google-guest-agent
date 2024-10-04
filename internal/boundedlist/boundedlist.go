//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package boundedlist implements a fixed size circular list. Adding new
// elements when the list is full will overwrite the oldest existing elements.
package boundedlist

// List represents a fixed size list.
type List[T any] struct {
	entries []T
	size    uint
	idx     uint
	isFull  bool
}

// New creates a new BoundedList with a given [size].
func New[T any](size uint) *List[T] {
	return &List[T]{
		entries: make([]T, size),
		size:    size,
	}
}

// Add adds an entry to the list. If list if full it overwrites entries like a
// circular list.
func (l *List[T]) Add(entry T) {
	l.entries[l.idx] = entry
	l.idx = (l.idx + 1) % l.size
	if l.idx == 0 {
		l.isFull = true
	}
}

// Capacity returns the maximum allowed entries in the list.
func (l *List[T]) Capacity() uint {
	return l.size
}

// Len returns the current number of entries in the list.
func (l *List[T]) Len() uint {
	if l.isFull {
		return l.size
	}
	return l.idx
}

// Item returns the entry at the given index.
func (l *List[T]) Item(idx uint) T {
	var entry T
	if idx >= l.Len() {
		return entry
	}

	if l.isFull {
		return l.entries[(l.idx+idx)%l.size]
	}
	return l.entries[idx]
}

// All returns all entries in the list.
func (l *List[T]) All() []T {
	size := l.Len()
	list := make([]T, size)
	for i := uint(0); i < size; i++ {
		list[i] = l.Item(i)
	}
	return list
}

// Reset flushes and resets the list.
func (l *List[T]) Reset() {
	l.isFull = false
	l.idx = 0
	l.entries = make([]T, l.size)
}

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

package lru

import (
	"testing"
)

func TestCapacity(t *testing.T) {
	want := uint(2)
	maxPut := want * 2
	cache := New[uint](want)

	for i := uint(1); i <= maxPut; i++ {
		cache.Put(i, i)

		// Try reinserting the same key.
		cache.Put(i, i*10)

		// Value should have been updated/changed.
		if val, ok := cache.Get(i); !ok || val != i*10 {
			t.Errorf("cache.Get(%v) = (%v, %v), want (%v, %v)", i, val, ok, i*10, true)
		}
	}

	if cache.Len() != want {
		t.Errorf("cache.Len() = %v, want %v", cache.Len(), want)
	}

	// The values [1..want] should not be in the cache.
	for i := uint(1); i <= want; i++ {
		if _, ok := cache.Get(i); ok {
			t.Errorf("cache.Get(%v) = true, want false", i)
		}
	}
}

func TestPromotion(t *testing.T) {
	cache := New[int](2)
	cache.Put(1, 1)
	cache.Put(2, 2)

	// Promote 1 to the front of the cache so that 2 is to be evicted.
	cache.Get(1)
	cache.Put(3, 3)

	// At this point 1 should exist in the cache and 2 should have been evicted.
	if _, ok := cache.Get(1); !ok {
		t.Errorf("cache.Get(1) = false, want true")
	}

	if _, ok := cache.Get(2); ok {
		t.Errorf("cache.Get(2) = true, want false")
	}

	if _, ok := cache.Get(3); !ok {
		t.Errorf("cache.Get(3) = false, want true")
	}
}

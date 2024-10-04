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

package boundedlist

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

type testData struct {
	timestamp *tpb.Timestamp
	usage     []int64
}

func TestAdd(t *testing.T) {
	list := New[testData](3)

	want := []testData{
		{timestamp: &tpb.Timestamp{Seconds: 1}, usage: []int64{4}},
		{timestamp: &tpb.Timestamp{Seconds: 2}, usage: []int64{5}},
		{timestamp: &tpb.Timestamp{Seconds: 3}, usage: []int64{6}},
	}

	add := []testData{
		{timestamp: &tpb.Timestamp{Seconds: 7}, usage: []int64{9}},
		{timestamp: &tpb.Timestamp{Seconds: 8}, usage: []int64{10}},
	}

	want2 := []testData{want[2]}
	want2 = append(want2, add...)

	want3 := []testData{
		{timestamp: &tpb.Timestamp{Seconds: 11}, usage: []int64{14}},
		{timestamp: &tpb.Timestamp{Seconds: 12}, usage: []int64{15}},
		{timestamp: &tpb.Timestamp{Seconds: 13}, usage: []int64{16}},
	}

	tests := []struct {
		want    []testData
		add     []testData
		wantLen uint
	}{
		{
			add: []testData{}, want: []testData{}, wantLen: 0,
		},
		{
			add: want, want: want, wantLen: 3,
		},
		{
			add: add, want: want2, wantLen: 3,
		},
		{
			add: want3, want: want3, wantLen: 3,
		},
	}

	// These tests are expected to run sequentially and on-purpose not executed
	// within own subtests.
	for _, test := range tests {
		for _, i := range test.add {
			list.Add(i)
		}
		if diff := cmp.Diff(test.want, list.All(), cmp.AllowUnexported(testData{}), protocmp.Transform()); diff != "" {
			t.Errorf("Add() returned diff (-want +got):\n%s", diff)
		}
		if list.Len() != test.wantLen {
			t.Errorf("Len() = %d elements, want %d", list.Len(), test.wantLen)
		}
	}
}

func TestReset(t *testing.T) {
	list := New[string](2)

	want := []string{"hello", "world"}
	for _, i := range want {
		list.Add(i)
	}
	if diff := cmp.Diff(want, list.All()); diff != "" {
		t.Errorf("Add() returned diff (-want +got):\n%s", diff)
	}
	if list.Len() != 2 {
		t.Errorf("Len() = %d elements, want %d", list.Len(), 2)
	}
	if !list.isFull {
		t.Errorf("isFull() = false, want true")
	}

	list.Reset()
	if list.Len() != 0 {
		t.Errorf("Len() = %d elements, want 0", list.Len())
	}
	if list.isFull {
		t.Errorf("isFull() = true, want false")
	}

	want = []string{}
	if diff := cmp.Diff(want, list.All()); diff != "" {
		t.Errorf("Add() returned diff (-want +got):\n%s", diff)
	}
}

func TestItem(t *testing.T) {
	list := New[int](3)

	want := []int{7, 8}
	for _, i := range want {
		list.Add(i)
	}

	if list.Len() != 2 {
		t.Errorf("Len() = %d elements, want %d", list.Len(), 2)
	}

	tests := []struct {
		desc string
		idx  uint
		want int
	}{
		{
			desc: "valid_0th_idx",
			idx:  0,
			want: 7,
		},
		{
			desc: "valid_1st_idx",
			idx:  1,
			want: 8,
		},
		{
			desc: "valid_2nd_idx_empty",
			idx:  2,
			want: 0,
		},
		{
			desc: "invalid_idx_outofbound",
			idx:  3,
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := list.Item(tc.idx); got != tc.want {
				t.Errorf("Item(%d) = %d, want %d", tc.idx, got, tc.want)
			}
		})
	}
}

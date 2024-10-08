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

package ini

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type configSection struct {
	Core *coreSection
}

type coreSection struct {
	Value1 string
	Value2 string
}

func TestWriteIniFile(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		data    any
		wantErr bool
	}{
		{
			name:    "invalid-data-ptr",
			file:    "/dev/ini-file",
			wantErr: true,
			data:    nil,
		},
		{
			name:    "not-a-pointer-to-struct",
			file:    "/dev/ini-file",
			wantErr: true,
			data:    configSection{},
		},
		{
			name:    "invalid-file",
			file:    "/dev/null/ini-file",
			wantErr: true,
			data:    &configSection{Core: &coreSection{Value1: "value1", Value2: "value2"}},
		},
		{
			name:    "success",
			file:    filepath.Join(t.TempDir(), "ini-file"),
			wantErr: false,
			data:    &configSection{Core: &coreSection{Value1: "value1", Value2: "value2"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteIniFile(tc.file, tc.data)
			if (err == nil) == tc.wantErr {
				t.Fatalf("WriteIniFile(%v, %v) = %v, want %v", tc.file, tc.data, err, tc.wantErr)
			}

			stat, err := os.Stat(tc.file)
			if (err == nil) == tc.wantErr {
				t.Fatalf("os.Stat(%v) = %v, want %v", tc.file, err, tc.wantErr)
			}

			if !tc.wantErr && stat.Size() == 0 {
				t.Errorf("os.Stat(%v).Size() = 0, want > 0", tc.file)
			}
		})
	}
}

func TestReflectFrom(t *testing.T) {
	tests := []struct {
		name    string
		data    any
		wantErr bool
	}{
		{
			name:    "invalid-data-ptr",
			wantErr: true,
			data:    nil,
		},
		{
			name:    "not-a-pointer-to-struct",
			wantErr: true,
			data:    configSection{},
		},
		{
			name:    "invalid-file",
			wantErr: false,
			data:    &configSection{Core: &coreSection{Value1: "value1", Value2: "value2"}},
		},
		{
			name:    "success",
			wantErr: false,
			data:    &configSection{Core: &coreSection{Value1: "value1", Value2: "value2"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inicfg, err := ReflectFrom(tc.data)
			if (err == nil) == tc.wantErr {
				t.Fatalf("ReflectFrom(%v) = %v, want %v", tc.data, err, tc.wantErr)
			}

			if tc.data == nil && err != nil {
				if !errors.Is(err, ErrInvalidData) {
					t.Errorf("ReflectFrom(%v) = %v, want %v", tc.data, err, ErrInvalidData)
				}
			}

			if !tc.wantErr && inicfg == nil {
				t.Errorf("ReflectFrom(%v) = %v, want non-nil", tc.data, inicfg)
			}
		})
	}
}

func TestReadIniFile(t *testing.T) {
	tests := []struct {
		name    string
		source  any
		data    any
		wantErr bool
	}{
		{
			name:    "invalid-data-ptr",
			source:  "/dev/ini-file",
			wantErr: true,
			data:    nil,
		},
		{
			name:    "invalid-file",
			source:  nil,
			wantErr: true,
			data:    &configSection{},
		},
		{
			name:    "not-a-pointer-to-struct",
			source:  "/dev/ini-file",
			wantErr: true,
			data:    configSection{},
		},
		{
			name:    "success",
			source:  "/dev/ini-file",
			wantErr: false,
			data:    &configSection{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ReadIniFile(tc.source, tc.data)
			if (err == nil) == tc.wantErr {
				t.Fatalf("ReadIniFile(%v, %v) = %v, want %v", tc.source, tc.data, err, tc.wantErr)
			}
		})
	}
}

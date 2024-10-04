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

//go:build linux

package reg

import (
	"testing"
)

func TestRegistryLinux(t *testing.T) {
	val, err := ReadMultiString("test", "name")
	if err == nil {
		t.Error("ReadMultiString(\"test\", \"name\") = nil, want non-nil")
	}

	if val != nil {
		t.Errorf("ReadMultiString(\"test\", \"name\") = %v, want nil", val)
	}

	if err := WriteMultiString("key", "name", []string{"value"}); err == nil {
		t.Error("writeMultiString(\"key\", \"name\", []string{\"value\"}) = nil, want non-nil")
	}
}

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

//go:build windows

// Package reg provides wrapper functions/utilities to read and write
// windows registry values.
package reg

import (
	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/sys/windows/registry"
)

const (
	// GCEKeyBase is the gce's base/parent registry key used to store the
	// diagnostics entries.
	GCEKeyBase = `SOFTWARE\Google\ComputeEngine`
)

// ReadString reads a single string value from the given registry key.
func ReadString(key, name string) (string, error) {
	galog.V(3).Debugf("Reading string value %q from registry key %q", name, key)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()

	s, _, err := k.GetStringValue(name)
	return s, err
}

// WriteString writes a single string value to the given registry key.
func WriteString(key, name, value string) error {
	galog.V(3).Debugf("Writing string value %q to registry key %q", name, key)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.WRITE)
	if err != nil {
		return err
	}
	defer k.Close()

	return k.SetStringValue(name, value)
}

// ReadMultiString reads a multi-string value from the given registry key.
func ReadMultiString(key, name string) ([]string, error) {
	galog.V(3).Debugf("Reading multi-stringvalue %q from registry key %q", name, key)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()

	s, _, err := k.GetStringsValue(name)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// WriteMultiString writes a multi-string value to the given registry key.
func WriteMultiString(key, name string, value []string) error {
	galog.V(3).Debugf("Writing multi-string value %q to registry key %q", name, key)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.WRITE)
	if err != nil {
		return err
	}
	defer k.Close()

	return k.SetStringsValue(name, value)
}

// Delete deletes the given registry key.
func Delete(name string) error {
	galog.V(3).Debugf("Deleting registry key %q", name)
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, name); err != nil {
		return err
	}
	return nil
}

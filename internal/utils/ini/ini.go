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

// Package ini provides wrapper util functions to parse and write INI files.
package ini

import (
	"errors"
	"fmt"

	"github.com/go-ini/ini"
)

var (
	// ErrInvalidData is returned when the data pointer is nil.
	ErrInvalidData = errors.New("invalid data pointer, ptr is nil")
)

// LoadOptions is a wrapper around ini.LoadOptions.
type LoadOptions = ini.LoadOptions

// WriteIniFile writes ptr data into filePath file marshalled in a ini file
// format.
func WriteIniFile(filePath string, ptr any, opts ...LoadOptions) error {
	if ptr == nil {
		return ErrInvalidData
	}

	config := ini.Empty(opts...)

	if err := ini.ReflectFrom(config, ptr); err != nil {
		return fmt.Errorf("error marshalling file: %w", err)
	}

	if err := config.SaveTo(filePath); err != nil {
		return fmt.Errorf("error saving file: %w", err)
	}

	return nil
}

// ReflectFrom reflects ptr data into an ini.File.
func ReflectFrom(ptr any, opts ...LoadOptions) (*ini.File, error) {
	if ptr == nil {
		return nil, ErrInvalidData
	}

	config := ini.Empty(opts...)

	if err := ini.ReflectFrom(config, ptr); err != nil {
		return nil, fmt.Errorf("error marshalling file: %w", err)
	}

	return config, nil
}

// ReadIniFile reads and parses the content of source and loads it into ptr. The
// source can be a file path or a byte array - in general it complies with
// ini.LoadSources() sources interface.
func ReadIniFile(source any, ptr any) error {
	if ptr == nil {
		return ErrInvalidData
	}

	opts := ini.LoadOptions{
		Loose:        true,
		Insensitive:  true,
		AllowShadows: true,
	}

	config, err := ini.LoadSources(opts, source)
	if err != nil {
		return fmt.Errorf("failed to load file: %w", err)
	}

	// Parse the ini.
	if err = config.MapTo(ptr); err != nil {
		return fmt.Errorf("error parsing file: %w", err)
	}

	return nil
}

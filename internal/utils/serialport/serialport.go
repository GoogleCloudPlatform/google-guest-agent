//  Copyright 2025 Google LLC
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

// Package serialport provides a serial port logger. This should not be used
// unless absolutely necessary. Currently, this is only used for writing
// credentials to the serial port on Windows.
package serialport

import "go.bug.st/serial"

// Writer is a serial port logger.
type Writer struct {
	// Port is the serial port to write to.
	Port string
	// IsTest is true if the logger is being used for testing.
	IsTest bool
}

// Write writes the given data to the serial port.
func (w *Writer) Write(b []byte) (int, error) {
	// For testing purposes, we don't want to open the serial port.
	if w.IsTest {
		return len(b), nil
	}

	mode := &serial.Mode{BaudRate: 115200}
	p, err := serial.Open(w.Port, mode)
	if err != nil {
		return 0, err
	}
	defer p.Close()
	return p.Write(b)
}

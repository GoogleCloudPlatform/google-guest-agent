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

// Package textconfig provides a way to read and write plain text configuration
// files (such as sshd.conf, pam.conf, etc).
package textconfig

import (
	"fmt"
	"os"
	"strings"
)

// Position is the position of a block in the file.
type Position int

const (
	// DefaultSpacer is the default spacer used to separate key & value when
	// writing the file.
	DefaultSpacer = " "

	// Top is the position identifier for the top of the file.
	Top Position = iota
	// Bottom is the position identifier for the bottom of the file.
	Bottom
)

// Entry is the configuration entry, it indicates the key and value of the
// entry.
type Entry struct {
	// key is the key of the entry.
	key string
	// value is the value of the entry.
	value string
}

// NewEntry creates a new entry.
func NewEntry(key, value string) *Entry {
	return &Entry{key: key, value: value}
}

// format returns the formatted entry string (applying the provided spacer
// between the key and value).
func (en *Entry) format(spacer string) string {
	return strings.TrimSpace(fmt.Sprintf("%s%s%s", en.key, spacer, en.value))
}

// Block is a group of entries that are in the same position in the file.
type Block struct {
	// entries is the list of entries in the block.
	entries []*Entry
	// position is the position of the block in the file (top or bottom).
	position Position
}

// NewBlock creates a new block for the given position.
func NewBlock(pos Position) *Block {
	return &Block{position: pos}
}

// Append appends an entry to the block.
func (bl *Block) Append(key, value string) {
	bl.entries = append(bl.entries, &Entry{key, value})
}

// lines returns the formatted lines of the block.
func (bl *Block) lines(delimiter *Delimiter, spacer string) []string {
	var lines []string

	// Block without entries, no need to write anything - avoid writing empty
	// blocks (with delimiters only).
	if len(bl.entries) == 0 {
		return nil
	}

	lines = append(lines, delimiter.Start)
	for _, entry := range bl.entries {
		lines = append(lines, entry.format(spacer))
	}
	lines = append(lines, delimiter.End)

	return lines
}

// Delimiter is the delimiter for a block. It indicates the block start matching
// pattern and the block end matching pattern.
type Delimiter struct {
	// Start is the start matching pattern.
	Start string
	// End is the end matching pattern.
	End string
}

// Handle is represents the configuration file.
type Handle struct {
	// file is the path to the configuration file.
	file string
	// mode is the mode of the file.
	mode os.FileMode
	// blocks is the list of blocks in the file.
	blocks []*Block
	// opts is the options for the configuration file.
	opts Options
}

// Options is the options for the configuration file.
type Options struct {
	// Delimiters is the default delimiter for the file, it is used when writing
	// the file - it's also considered for reading and identifying blocks of
	// interest along with the known delimiters (ever supported delimiters).
	Delimiters *Delimiter
	// KnownDelimiters is the list of known delimiters for the file i.e. previously
	// used delimiters and still supported for backwards compatibility.
	KnownDelimiters []*Delimiter
	// AllDelimiters is the list of all delimiters for the file, it's ordered as:
	// - Delimiters
	// - KnownDelimiters
	AllDelimiters []*Delimiter
	// Spacer is the spacer used to separate key & value when writing the file.
	Spacer string
	// deprecatedEntries is the list of deprecated entries that should be removed
	// from the file.
	DeprecatedEntries []*Entry
}

// New creates a new configuration file handle.
func New(file string, mode os.FileMode, opts Options) *Handle {
	var allDelimiters []*Delimiter

	allDelimiters = append(allDelimiters, opts.Delimiters)
	allDelimiters = append(allDelimiters, opts.KnownDelimiters...)

	for _, dd := range allDelimiters {
		if dd == nil {
			continue
		}
		opts.AllDelimiters = append(opts.AllDelimiters, dd)
	}

	if opts.Spacer == "" {
		opts.Spacer = DefaultSpacer
	}

	return &Handle{file: file, mode: mode, opts: opts}
}

// AddBlock adds a block to the configuration handle.
func (h *Handle) AddBlock(block *Block) {
	h.blocks = append(h.blocks, block)
}

// Cleanup removes the blocks and deprecated entries managed by the Handle from
// the file.
func (h *Handle) Cleanup() error {
	data, err := os.ReadFile(h.file)
	if err != nil {
		return fmt.Errorf("failed to read file %q: %v", h.file, err)
	}

	lines := strings.Split(string(data), "\n")
	output := h.cleanup(lines)

	if err := os.WriteFile(h.file, []byte(strings.Join(output, "\n")), h.mode); err != nil {
		return fmt.Errorf("failed to write file %q: %v", h.file, err)
	}

	return nil
}

// cleanup removes our managed blocks.
func (h *Handle) cleanup(lines []string) []string {
	var output []string
	inBlock := false

	for _, line := range lines {
		isMatch := h.matchLine(line)

		// Leave out the delimiters.
		if isMatch {
			inBlock = !inBlock
			continue
		}

		// Leave out any lines inside a block.
		if inBlock {
			continue
		}

		// Leave out any deprecated entries.
		foundDeprecatedEntry := false
		for _, entry := range h.opts.DeprecatedEntries {
			if strings.HasPrefix(strings.TrimSpace(line), entry.key) && strings.HasSuffix(strings.TrimSpace(line), entry.value) {
				foundDeprecatedEntry = true
				break
			}
		}
		if foundDeprecatedEntry {
			continue
		}

		output = append(output, line)
	}

	return output
}

// matchLine returns true if the line matches any of the delimiters.
func (h *Handle) matchLine(line string) bool {
	for _, dd := range h.opts.AllDelimiters {
		if strings.TrimSpace(line) == dd.Start {
			return true
		}
		if strings.TrimSpace(line) == dd.End {
			return true
		}
	}
	return false
}

// Apply applies the changes to the file.
func (h *Handle) Apply() error {
	if err := h.Cleanup(); err != nil {
		return fmt.Errorf("failed to cleanup file %q: %v", h.file, err)
	}

	var topBlocks []*Block
	var bottomBlocks []*Block

	for _, block := range h.blocks {
		switch block.position {
		case Top:
			topBlocks = append(topBlocks, block)
		case Bottom:
			bottomBlocks = append(bottomBlocks, block)
		}
	}

	data, err := os.ReadFile(h.file)
	if err != nil {
		return fmt.Errorf("failed to read file %q: %v", h.file, err)
	}

	lines := strings.Split(string(data), "\n")
	cleanedup := h.cleanup(lines)
	var output []string

	for _, block := range topBlocks {
		output = append(output, block.lines(h.opts.Delimiters, h.opts.Spacer)...)
	}

	output = append(output, cleanedup...)

	for _, block := range bottomBlocks {
		output = append(output, block.lines(h.opts.Delimiters, h.opts.Spacer)...)
	}

	if err := os.WriteFile(h.file, []byte(strings.Join(output, "\n")), h.mode); err != nil {
		return fmt.Errorf("failed to write file %q: %v", h.file, err)
	}

	return nil
}

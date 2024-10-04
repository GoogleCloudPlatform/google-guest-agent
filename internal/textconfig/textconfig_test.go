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

package textconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanup(t *testing.T) {
	commonDelimiter := &Delimiter{
		Start: "# start our block",
		End:   "# end our block",
	}

	tests := []struct {
		name              string
		deprecatedEntries []*Entry
		data              string
		want              string
		delimiter         *Delimiter
	}{
		{
			name: "empty",
			data: "",
			want: "",
		},
		{
			name: "empty-top-block",
			data: `
			# start our block
			# end our block
			zzZZzzzZZzz
			`,
			want: `
			zzZZzzzZZzz
			`,
			delimiter: commonDelimiter,
		},
		{
			name: "filled-top-block",
			data: `
			# start our block
			key value
			key value
			key2 value
			# end our block
			zzZZzzzZZzz
			`,
			want: `
			zzZZzzzZZzz
			`,
			delimiter: commonDelimiter,
		},
		{
			name: "start-top-block-without-end",
			data: `
			# start our block
			key value
			key value
			key2 value
			zzZZzzzZZzz
			`,
			want:      "",
			delimiter: commonDelimiter,
		},
		{
			name: "empty-bottom-block",
			data: `
			zzZZzzzZZzz
			# start our block
			# end our block
			`,
			want: `
			zzZZzzzZZzz
			`,
			delimiter: commonDelimiter,
		},
		{
			name: "filled-bottom-block",
			data: `
			zzZZzzzZZzz
			# start our block
			key value
			key value
			key2 value
			# end our block
			`,
			want: `
			zzZZzzzZZzz
			`,
			delimiter: commonDelimiter,
		},
		{
			name: "start-bottom-block-without-end",
			data: `
			zzZZzzzZZzz
			# start our block
			key value
			key value
			key2 value
			`,
			want: `
			zzZZzzzZZzz`,
			delimiter: commonDelimiter,
		},
		{
			name: "deprecated-entry",
			deprecatedEntries: []*Entry{
				{
					key:   "deprecatedKey",
					value: "deprecatedValue",
				},
			},
			data: `
			zzZZzzzZZzz
			deprecatedKey deprecatedValue
			`,
			want: `
			zzZZzzzZZzz
			`,
			delimiter: commonDelimiter,
		},
		{
			name: "deprecated-key-no-value",
			deprecatedEntries: []*Entry{
				{
					key:   "deprecatedKey",
					value: "deprecatedValue",
				},
			},
			data: `
			zzZZzzzZZzz
			deprecatedKey
			`,
			want: `
			zzZZzzzZZzz
			deprecatedKey
			`,
			delimiter: commonDelimiter,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "test.txt")
			fileMode := os.FileMode(0644)

			if err := os.WriteFile(filePath, []byte(tc.data), fileMode); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			opts := Options{Delimiters: tc.delimiter, DeprecatedEntries: tc.deprecatedEntries}
			cfg := New(filePath, fileMode, opts)

			if err := cfg.Cleanup(); err != nil {
				t.Fatalf("Failed to cleanup test file: %v", err)
			}

			data1, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("Failed to read test file: %v", err)
			}

			if string(data1) != tc.want {
				t.Errorf("Cleanup() = %q, want %q", string(data1), tc.want)
			}

			// A second cleanup should not produce a different result.
			if err := cfg.Cleanup(); err != nil {
				t.Fatalf("Failed to cleanup test file: %v", err)
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("Failed to read test file: %v", err)
			}

			if string(data) != tc.want {
				t.Errorf("Cleanup() = %q, want %q", string(data), tc.want)
			}

			if string(data1) != string(data) {
				t.Errorf("Cleanup() = %q, want %q", string(data), string(data1))
			}
		})
	}
}

func TestApply(t *testing.T) {
	commonDelimiter := &Delimiter{
		Start: "# start our block",
		End:   "# end our block",
	}

	tests := []struct {
		name          string
		data          string
		topEntries    []Entry
		bottomEntries []Entry
		want          string
		delimiter     *Delimiter
	}{
		{
			name:          "empty",
			data:          "",
			topEntries:    []Entry{},
			bottomEntries: []Entry{},
			delimiter:     commonDelimiter,
			want:          "",
		},
		{
			name: "existing-config-no-new-entries",
			data: `originalData1 value1
			originalData2 value2
			originalData3 value3`,
			topEntries:    []Entry{},
			bottomEntries: []Entry{},
			delimiter:     commonDelimiter,
			want: `originalData1 value1
			originalData2 value2
			originalData3 value3`,
		},
		{
			name: "existing-config-new-single-top-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries:    []Entry{{"newKey", "newValue"}},
			bottomEntries: []Entry{},
			delimiter:     commonDelimiter,
			want: `# start our block
newKey newValue
# end our block
originalData1 value1
originalData2 value2
originalData3 value3`,
		},
		{
			name: "existing-config-new-multi-top-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries: []Entry{
				{"newKey", "newValue"},
				{"newKey2", "newValue2"},
				{"newKey3", "newValue3"},
			},
			bottomEntries: []Entry{},
			delimiter:     commonDelimiter,
			want: `# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block
originalData1 value1
originalData2 value2
originalData3 value3`,
		},
		{
			name: "existing-config-new-single-bottom-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries:    []Entry{},
			bottomEntries: []Entry{{"newKey", "newValue"}},
			delimiter:     commonDelimiter,
			want: `originalData1 value1
originalData2 value2
originalData3 value3
# start our block
newKey newValue
# end our block`,
		},
		{
			name: "existing-config-new-multi-bottom-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries: []Entry{},
			bottomEntries: []Entry{
				{"newKey", "newValue"},
				{"newKey2", "newValue2"},
				{"newKey3", "newValue3"},
			},
			delimiter: commonDelimiter,
			want: `originalData1 value1
originalData2 value2
originalData3 value3
# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block`,
		},
		{
			name: "existing-config-new-single-top-bottom-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries:    []Entry{{"newKey", "newValue"}},
			bottomEntries: []Entry{{"newKey", "newValue"}},
			delimiter:     commonDelimiter,
			want: `# start our block
newKey newValue
# end our block
originalData1 value1
originalData2 value2
originalData3 value3
# start our block
newKey newValue
# end our block`,
		},
		{
			name: "existing-config-new-multi-top-bottom-entry",
			data: `originalData1 value1
originalData2 value2
originalData3 value3`,
			topEntries: []Entry{
				{"newKey", "newValue"},
				{"newKey2", "newValue2"},
				{"newKey3", "newValue3"},
			},
			bottomEntries: []Entry{
				{"newKey", "newValue"},
				{"newKey2", "newValue2"},
				{"newKey3", "newValue3"},
			},
			delimiter: commonDelimiter,
			want: `# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block
originalData1 value1
originalData2 value2
originalData3 value3
# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "test.txt")
			fileMode := os.FileMode(0644)

			if err := os.WriteFile(filePath, []byte(tc.data), fileMode); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			opts := Options{Delimiters: tc.delimiter}
			cfg := New(filePath, fileMode, opts)

			if tc.topEntries != nil {
				topBlock := NewBlock(Top)
				for _, entry := range tc.topEntries {
					topBlock.Append(entry.key, entry.value)
				}
				cfg.AddBlock(topBlock)
			}

			if tc.bottomEntries != nil {
				bottomBlock := NewBlock(Bottom)
				for _, entry := range tc.bottomEntries {
					bottomBlock.Append(entry.key, entry.value)
				}
				cfg.AddBlock(bottomBlock)
			}

			if err := cfg.Apply(); err != nil {
				t.Fatalf("Failed to apply test file: %v", err)
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("Failed to read test file: %v", err)
			}

			if string(data) != tc.want {
				t.Errorf("Cleanup() = %q, want %q", string(data), tc.want)
			}
		})
	}
}

func TestCleanupAndApply(t *testing.T) {
	originalData := `originalData1 value1
originalData2 value2
originalData3 value3`

	want := `# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block
originalData1 value1
originalData2 value2
originalData3 value3
# start our block
newKey newValue
newKey2 newValue2
newKey3 newValue3
# end our block`

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	fileMode := os.FileMode(0644)

	if err := os.WriteFile(filePath, []byte(originalData), fileMode); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	opts := Options{Delimiters: &Delimiter{Start: "# start our block", End: "# end our block"}}
	cfg := New(filePath, fileMode, opts)

	topBlock := NewBlock(Top)
	topBlock.Append("newKey", "newValue")
	topBlock.Append("newKey2", "newValue2")
	topBlock.Append("newKey3", "newValue3")
	cfg.AddBlock(topBlock)

	bottomBlock := NewBlock(Bottom)
	bottomBlock.Append("newKey", "newValue")
	bottomBlock.Append("newKey2", "newValue2")
	bottomBlock.Append("newKey3", "newValue3")
	cfg.AddBlock(bottomBlock)

	if err := cfg.Apply(); err != nil {
		t.Errorf("Failed to apply test file: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}

	if string(data) != want {
		t.Errorf("Apply() = %q, want %q", string(data), want)
	}

	if err := cfg.Cleanup(); err != nil {
		t.Errorf("Failed to cleanup test file: %v", err)
	}

	// Applying again should not produce a different result. We want to make sure
	// that new lines are not added indiscriminately to the end of the file.
	if err := cfg.Apply(); err != nil {
		t.Errorf("Failed to apply test file: %v", err)
	}

	data, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}

	if string(data) != want {
		t.Errorf("Apply() = %q, want %q", string(data), want)
	}
}

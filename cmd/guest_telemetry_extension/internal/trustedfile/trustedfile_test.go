/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package trustedfile

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates the named file with the given contents and permissions.
func writeFile(t *testing.T, name, contents string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, perm); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileErrors(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dir, filepath.Join(dir, "missing")} {
		if _, err := ReadFile(name); err == nil {
			t.Errorf("ReadFile(%q) succeeded, want error", name)
		}
	}
}

func TestCheckPathMissingFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "missing")
	if got, err := CheckPath(name); err == nil {
		t.Errorf("CheckPath(%q) = %q, want error", name, got)
	}
}

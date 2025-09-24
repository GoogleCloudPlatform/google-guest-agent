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

//go:build windows

package workloadcertrefresh

import (
	"fmt"
	"strings"
)

const (
	// contentDirPrefix is used as prefix to create certificate directories on
	// refresh as contentDirPrefix-<time>.
	contentDirPrefix = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-contents`
	// tempSymlinkPrefix is used as prefix to create temporary symlinks on refresh
	// as tempSymlinkPrefix-<time> to content directories.
	tempSymlinkPrefix = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-symlink`
	// symlink points to the directory with current GCE workload certificates and
	// is always expected to be present.
	symlink = `C:\ProgramData\Google\ComputeEngine\secrets\workload-spiffe-credentials`
)

func (j *RefresherJob) generateTmpDirNames(opts outputOpts, now string) (string, string) {
	// Replace all colons with dashes to create a valid filename for Windows.
	validFilename := strings.ReplaceAll(now, ":", "-")
	contentDir := fmt.Sprintf("%s-%s", opts.contentDirPrefix, validFilename)
	tempSymlink := fmt.Sprintf("%s-%s", opts.tempSymlinkPrefix, validFilename)
	return contentDir, tempSymlink
}

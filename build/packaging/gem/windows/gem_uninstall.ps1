#  Copyright 2026 Google LLC
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

$install_dir = "${env:ProgramFiles}\Google\Extension Manager"
$exe = "${install_dir}\google_extension_manager.exe"
Stop-Service GoogleExtensionManager -ErrorAction SilentlyContinue -Verbose
if (Test-Path $exe) {
  & $exe -cleanup
}
& sc.exe delete GoogleExtensionManager
if ((Test-Path $install_dir) -and ([IO.Path]::GetFullPath($PSScriptRoot) -ne [IO.Path]::GetFullPath($install_dir))) {
  Remove-Item -Recurse -Force -Path $install_dir -ErrorAction SilentlyContinue
}

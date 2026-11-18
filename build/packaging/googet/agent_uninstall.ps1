#  Copyright 2017 Google LLC
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      https://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

function Remove-ServiceSafely {
    param (
        [string]$ServiceName
    )
    $service = Get-Service $ServiceName -ErrorAction SilentlyContinue
    if ($service) {
        if ($service.Status -ne 'Stopped') {
            try {
                Stop-Service $ServiceName -Force -ErrorAction Stop
            } catch {
                Write-Warning "Failed to stop service $ServiceName : $_"
            }
        }
        try {
            $output = & sc.exe delete $ServiceName 2>&1
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "sc.exe delete failed for $ServiceName : $output"
            }
        } catch {
            Write-Warning "Failed to delete service $ServiceName : $_"
        }
    }
}

# Stop and delete GCEAgent.
Remove-ServiceSafely -ServiceName 'GCEAgent'

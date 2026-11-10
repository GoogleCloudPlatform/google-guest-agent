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

$name = 'GoogleExtensionManager'
$install_dir = "${env:ProgramFiles}\Google\Extension Manager"
$exe_path = "${install_dir}\google_extension_manager.exe"
$path = "`"${exe_path}`""
$display_name = 'Google Extension Manager'
$description = 'Standalone Google Extension Manager daemon'
$config_dir = "${env:ProgramData}\Google\GEM"
$config = "${config_dir}\gem.cfg"
$machine_id_file = "${config_dir}\machine_id"
$initial_config = @'
# Google Extension Manager (GEM) Configuration
# Uncomment and set values below to override compiled-in defaults.

[Core]
# cloud_logging_enabled = false
# enable_local_plugins = false
# mds_enabled = false
# on_demand_plugins = true
# acs_client = true
# agent_name = GoogleExtensionManager
# log_level = 3
# log_verbosity = 0
# log_file =

[Instance]
# Set instance_id here or write the identifier to C:\ProgramData\Google\GEM\machine_id
# (/etc/google-extension-manager/machine_id on Linux).
# instance_id =
# instance_id_dir = C:\ProgramData\Google\GEM

[PluginConfig]
# state_dir = C:\ProgramData\Google\GEM
# socket_connections_dir = C:\ProgramData\Google\GEM
# local_plugin_dir = C:\Program Files\Google\Extension Manager

[ACS]
# endpoint =
# channel_id =
# host =
# client_debug_logging = false
'@

function Set-ServiceConfig {
  sc.exe failure $name reset= 60 actions= restart/1000/restart/2000
  cmd.exe /c "sc.exe config ${name} start= delayed-auto binpath= \`"${path}\`""
  sc.exe triggerinfo $name start/networkon
}

try {
  if (-not (Test-Path $install_dir)) {
    New-Item -ItemType Directory -Force -Path $install_dir | Out-Null
  }
  $src_exe = Join-Path $PSScriptRoot 'google_extension_manager.exe'
  if ((Test-Path $src_exe) -and ([IO.Path]::GetFullPath($src_exe) -ne [IO.Path]::GetFullPath($exe_path))) {
    Stop-Service $name -ErrorAction SilentlyContinue
    Copy-Item -Force -Path $src_exe -Destination $exe_path
    $src_license = Join-Path $PSScriptRoot 'LICENSE'
    if (Test-Path $src_license) {
      Copy-Item -Force -Path $src_license -Destination (Join-Path $install_dir 'LICENSE.txt')
    }
  }

  if (-not (Test-Path $config_dir)) {
    New-Item -ItemType Directory -Force -Path $config_dir | Out-Null
  }
  if (-not (Test-Path $config)) {
    $initial_config | Set-Content -Path $config -Encoding ASCII
  }

  if (-not (Get-Service $name -ErrorAction SilentlyContinue)) {
    New-Service -Name $name `
                -DisplayName $display_name `
                -BinaryPathName $path `
                -StartupType Automatic `
                -Description $description
  }
  else {
    Set-Service -Name $name `
                -DisplayName $display_name `
                -Description $description
  }

  Set-ServiceConfig
  $has_machine_id = ((Test-Path $machine_id_file) -and ((Get-Content -Path $machine_id_file -Raw).Trim().Length -gt 0))
  $has_cfg_instance_id = ((Test-Path $config) -and [bool](Select-String -Path $config -Pattern '^\s*instance_id\s*=\s*[^\s#;]' -Quiet))
  if ($has_machine_id -or $has_cfg_instance_id) {
    Restart-Service $name -ErrorAction SilentlyContinue -Verbose
  }
  exit 0
}
catch {
  Write-Output $_.InvocationInfo.PositionMessage
  Write-Output "Install failed: $($_.Exception.Message)"
  exit 1
}

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

// Package versioncommands provides the commands to be executed to gather version information.
package versioncommands

// VersionCommands contains the command to be executed to gather version information.
type VersionCommands struct {
	Cmd []string
}

// Commands is the list of commands to be executed to gather version information.
var Commands *VersionCommands = &VersionCommands{
	// Order matters. The order here needs to match the order in the enum in definition.proto.
	Cmd: []string{
		"unspecified",
		"cat",
		"/usr/sbin/apache2",
		"/usr/sbin/httpd",
		"postgres",
		"psql",
		"nodetool",
		"mongod",
		"/usr/sbin/mysqld",
		"sqlplus",
		"redis-server",
		"mariadb",
		// wildcard because we don't know the SID.
		"/usr/sap/*/SYS/exe/run/gwrd",
		"grep",
		"Get-Command",
		"$IQDIR15/bin64/start_iq",
		"$IQDIR16/bin64/start_iq",
		"C:\\SAP\\IQ-16_0\\bin64\\iqsrv16.exe",
		"C:\\SAP\\IQ-15_0\\bin64\\iqsrv15.exe",
		"$(find /usr/sap -maxdepth 5 -name disp+work -print -quit)",
		"/usr/sbin/pacemakerd",
		"sqlservr",
		"Get-ItemPropertyValue",
		"$ORACLE_HOME/OPatch/opatch",
		"$ORACLE_HOME/bin/dgmgrl",
		"java",
		"$OGG_HOME/ggsci",
		"$PS_HOME/appserv/psadmin",
		"/hana/shared/WHP/hdblcm/hdblcm",
		"mysql",
		"/usr/share/cassandra/bin/nodetool",
		"/usr/sbin/nodetool",
		"/opt/mssql/bin/sqlservr",
		"USE_DISCOVERED_PROCESS_PATH",
	},
}

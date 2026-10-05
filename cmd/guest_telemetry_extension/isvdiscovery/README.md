# ISV Discovery

This directory contains the ISV Discovery module for the Guest Telemetry
Extension. This module is intended to enable the gathering of information
about Independent Software Vendors (ISVs) running on a VM.

## Edge Cases and Judgement Calls for Workload Detection

- We consider detecting the process named `httpd` to mean Apache Web Server is
present. This may result in false positives.
- We consider the process named `mysqld` to mean that MySQL is present. Older
versions of MariaDB also use this process name. We only consider MariaDB as a
present workload if the process named `mariadbd` is running.
- We consider the process named `memurai` to mean Redis is present. Memurai is
a Redis-compatible data store built to run natively on Windows.
- In many cases the best version command would require using a restricted
command line argument such as a pipe or semicolon. Consequently, we lack
functional version detection for certain workloads. Additionally,
some of the commands used are not what would be preferred without these
restrictions.
- Some of the version commands will need to be in the global PATH in order to work.
- Several products embed a complete OpenSearch node and launch it with the
stock `org.opensearch.bootstrap.OpenSearch` bootstrap class, among them the
Wazuh indexer, Graylog Data Node and Atlassian Bitbucket Server. We report
these as OpenSearch, because an OpenSearch node really is running on the VM. It
would be valuable to add detectors for these specific products in the future to
add clarity about when OpenSearch is running standalone versus when it is
running as a component of another product.
- OpenSearch installed as a Windows service runs under the Apache Commons
Daemon host process, which loads the JVM in-process, so the bootstrap class
never appears in the command line. We detect that case by the service
executable name instead, and we do not report a version for it.
- We do not report an OpenSearch version when the node runs in a container,
because the agent cannot read files from the container's filesystem.
- We originally planned to detect SAP System and SAP Web AS as two separate
workloads. However, SAP System is a cluster made up of several instances
running various workloads. To identify it, we'd really just be looking for the
process names that indicate SAP Web AS is running. As such, we just identify
SAP Web AS and do not identify SAP System.

## License and Copyright

Copyright 2025 Google LLC.

Apache License, Version 2.0

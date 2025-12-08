[Public Cloud Docs](https://docs.cloud.google.com/compute/docs/images/guest-agent)

## Guest Agent for Google Compute Engine.
This repository contains the source code and packaging artifacts for the Google
guest agent. These components are installed on Windows and Linux GCE VMs in
order to enable GCE platform features.

## Building Guest Agent
In the codebase there's a GNU Make based build system with targets to build and
test the guest agent.

In order to build both the `guest-agent` and `core_plugins`, run

```shell
make
```

To build only one or the other, run the respective `make` target:

```shell
make build cmd/google_guest_agent/google_guest_agent
```

```shell
make build cmd/core_plugin/core_plugin
```

To run all the go tests, run:

```shell
make test
```

For more targets and info about the build system run:

```shell
make help
```
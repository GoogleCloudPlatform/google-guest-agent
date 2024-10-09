TODO: Add link to external documentation whenever it is ready.

## Guest Agent for Google Compute Engine.
This repository contains the source code and packaging artifacts for the Google
guest agent. These components are installed on Windows and Linux GCE VMs in order to enable GCE platform features.

## Building Guest Agent
Included in the repository are some Makefiles to build and test the guest agent.

In order to build both the `guest-agent` and `core_plugins`, run

```shell
make
```

To build only one or the other, run the respective `make` target:

```shell
make buildagent
```

```shell
make buildcoreplugin
```

To run all the go tests, run

```shell
make test
```
# build/debugging
ifeq ($(V),1)
Q :=
else
Q := @
endif

include build/Makefile.gobin

clean: clean-go-binaries clean-pbgo
build: build-go-binaries
test: go-unit-tests

help:
	$(Q)echo  "VARIABLES:"
	$(Q)echo  "  V                 - Runs the build system in verbose mode"
	$(Q)echo  " "
	$(Q)echo  "GENERAL TARGETS:"
	$(Q)echo  "  build             - Builds all binary artifacts (all code generation also happens)"
	$(Q)echo  "  clean             - Cleans up binaries and generated code"

.PHONY: $(PHONY)
.DEFAULT_GOAL = build

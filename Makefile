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
check: go-lint

help:
	$(Q)echo  "VARIABLES:"
	$(Q)echo  "  V                 - Runs the build system in verbose mode i.e. V=1 make"
	$(Q)echo  " "
	$(Q)echo  "GENERAL TARGETS:"
	$(Q)echo  "  build             - Builds all binary artifacts (all code generation also happens)"
	$(Q)echo  "  check             - Runs linters and code checks"
	$(Q)echo  "  clean             - Cleans up binaries and generated code"
	$(Q)echo  "  help              - Prints this help message"
	$(Q)echo  "  test              - Runs all unit tests"

.PHONY: $(PHONY)
.DEFAULT_GOAL = build

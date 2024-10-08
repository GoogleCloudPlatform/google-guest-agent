BUILDPATH = cmd/core_plugin cmd/google_guest_agent

.PHONY: clean build buildagent buildcoreplugin test

build: clean buildagent buildcoreplugin

buildagent:
	${MAKE} -C cmd/google_guest_agent build
	mv cmd/google_guest_agent/google_guest_agent ./

buildcoreplugin:
	${MAKE} -C cmd/core_plugin build
	mv cmd/core_plugin/core_plugin ./

clean:
	$(foreach path, $(BUILDPATH), ${MAKE} -C $(path) clean;)
	rm -f google_guest_agent core_plugin;

test:
	go test ./...;

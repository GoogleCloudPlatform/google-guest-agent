PROTOPATH = cmd/core_plugin/snapshot/proto cmd/core_plugin/agentcrypto/proto internal/plugin/proto/ internal/acp/proto
BUILDPATH = cmd/core_plugin cmd/google_guest_agent
EXEPATH = cmd/core_plugin/core_plugin cmd/google_guest_agent/google_guest_agent

.PHONY: clean build test buildproto

build: buildproto buildagent buildcoreplugin

buildagent: buildproto
	${MAKE} -C cmd/google_guest_agent build
	mv cmd/google_guest_agent/google_guest_agent ./

buildcoreplugin: buildproto
	${MAKE} -C cmd/core_plugin build
	mv cmd/core_plugin/core_plugin ./

clean:
	$(foreach path, $(PROTOPATH), ${MAKE} -C $(path) clean;)
	$(foreach path, $(BUILDPATH), ${MAKE} -C $(path) clean;)
	rm -f google_guest_agent core_plugin;

test: buildproto
	go test ./...;
	$(foreach path, $(PROTOPATH), ${MAKE} -C $(path) clean;)

buildproto:
	$(foreach path, $(PROTOPATH), ${MAKE} -C $(path) buildproto;)

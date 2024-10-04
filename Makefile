PROTOPATH = cmd/core_plugin/snapshot/proto cmd/core_plugin/agentcrypto/proto internal/plugin/proto/ internal/acp/proto
BUILDPATH = cmd/core_plugin cmd/google_guest_agent

.PHONY: clean build test buildproto

build: buildproto
	$(foreach path, $(BUILDPATH), ${MAKE} -C $(path) build;)

clean:
	$(foreach path, $(PROTOPATH), ${MAKE} -C $(path) clean;)
	$(foreach path, $(BUILDPATH), ${MAKE} -C $(path) clean;)

test: buildproto
	go test ./...

buildproto:
	$(foreach path, $(PROTOPATH), ${MAKE} -C $(path) buildproto;)

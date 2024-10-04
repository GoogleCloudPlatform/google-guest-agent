.PHONY: buildproto clean

buildproto: agent_controlplane.proto
	protoc --go_out=. agent_controlplane.proto

clean:
	rm -rf google_guest_agent

.PHONY: buildproto clean

buildproto: plugin_comm.proto
	protoc --go_out=. --go-grpc_out=. plugin_comm.proto

clean:
	rm -rf google_guest_agent

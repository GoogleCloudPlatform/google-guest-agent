.PHONY: buildproto clean

buildproto:
	protoc --go_out=. --go-grpc_out=. snapshot_service.proto

clean:
	rm -rf cloud_vmm

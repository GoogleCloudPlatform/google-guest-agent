.PHONY: buildproto clean

buildproto:
	git clone https://github.com/google/go-tpm-tools
	protoc --go_out=. guest_credentials.proto -I.
	rm -rf go-tpm-tools

clean:
	rm -rf credentials go-tpm-tools

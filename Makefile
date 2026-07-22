.PHONY: build test clean

build:
	mkdir -p bin
	go build -trimpath -o bin/handoff ./cmd/handoff
	go build -trimpath -o bin/handoffd ./cmd/handoffd

test:
	go test ./...

clean:
	rm -rf bin

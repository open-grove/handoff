.PHONY: build test test-worker clean

build:
	mkdir -p bin
	go build -trimpath -o bin/handoff ./cmd/handoff
	go build -trimpath -o bin/handoffd ./cmd/handoffd

test:
	go test ./...

test-worker:
	npm test --prefix cloudflare

clean:
	rm -rf bin

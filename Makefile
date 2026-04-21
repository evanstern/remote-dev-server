.PHONY: coda-core test test-go test-shell clean

coda-core:
	go build -o ./coda-core ./cmd/coda-core/

test: test-go test-shell

test-go:
	go test ./...

test-shell:
	bash tests/run.sh

clean:
	rm -f ./coda-core

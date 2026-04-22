.PHONY: coda-core test test-go test-shell test-bats clean

coda-core:
	go build -o ./coda-core ./cmd/coda-core/

test: test-go test-shell test-bats

test-go:
	go test ./...

test-shell:
	bash tests/run.sh

test-bats: coda-core
	bats test/

clean:
	rm -f ./coda-core

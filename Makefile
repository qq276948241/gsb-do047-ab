.PHONY: verify build test check examples generate

# verify is the single entry point for clean-machine acceptance. It locks
# dependencies, builds, tests, checks the examples and regenerates their
# golden text, stopping at the first failing stage.
verify:
	./scripts/verify.sh

build:
	go build -o mdschema ./cmd/mdschema

test:
	go test ./...

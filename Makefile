# One-command acceptance: lock check, build, tests and example check/generate.
# On a clean machine run `make verify` and nothing else.
.PHONY: verify verify-update

verify:
	./scripts/verify.sh

verify-update:
	./scripts/verify.sh --update

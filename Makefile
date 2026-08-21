# The three commands used every day, mirroring spring-agent's Makefile. The
# module walk is what go.work buys: one `go build ./...` per module from the
# root, without -C flags or a script.

MODULES := core persistence/sqlx persistence/mongodb persistence/redis integration/feishu app cli

.DEFAULT_GOAL := build

.PHONY: build test lint

build:
	@for m in $(MODULES); do (cd $$m && go build ./...) || exit 1; done

test:
	@for m in $(MODULES); do (cd $$m && go test ./...) || exit 1; done

# gofmt -l prints the files it would rewrite; an empty list is the pass
# condition. go vet runs per module for the same reason build does.
lint:
	@out=$$(gofmt -l . 2>/dev/null); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

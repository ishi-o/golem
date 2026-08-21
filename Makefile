# The module walk is what go.work buys: one build/test per module from the
# root. Keeping it explicit makes an adapter's dependency boundary visible in
# the same place as the module list.

MODULES := core internal store/sqlx store/mongodb store/redis connector/feishu sandbox/docker sandbox/kubernetes app cmd test

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

MODULES := core internal store/sqlx store/mongodb store/redis connector/feishu sandbox/docker sandbox/kubernetes app cmd test

.DEFAULT_GOAL := build

.PHONY: build check test lint

build:
	@for m in $(MODULES); do (cd $$m && go build ./...) || exit 1; done

check:
	@out=$$(gofmt -l . 2>/dev/null); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

test:
	@go test ./test/...

lint:
	@gofmt -w .
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

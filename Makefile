BINARY := kong-loadgen
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test smoke dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./src

test:
	go test ./...

smoke: build
	sh scripts/smoke.sh

dist:
	@mkdir -p dist
	@for os in darwin linux; do \
	  for arch in amd64 arm64; do \
	    name=$(BINARY)-$$os-$$arch; \
	    tmp=$$(mktemp -d); \
	    GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $$tmp/$(BINARY) ./src; \
	    cp config.yaml apis.yaml README.md $$tmp/; \
	    cp -R docs $$tmp/; \
	    tar -czf dist/$$name.tar.gz -C $$tmp .; \
	    rm -rf $$tmp; \
	  done; \
	done

clean:
	rm -f $(BINARY)
	rm -rf dist

GO_BUILD_ENV :=
GO_BUILD_FLAGS :=
MODULE_BINARY := bin/capture-control-webapp
ENTRYPOINT := dist/index.html

ifeq ($(VIAM_TARGET_OS), windows)
	GO_BUILD_ENV += GOOS=windows GOARCH=amd64
	GO_BUILD_FLAGS := -tags no_cgo
	MODULE_BINARY = bin/capture-control-webapp.exe
endif

.DEFAULT_GOAL := all

$(ENTRYPOINT): package.json package-lock.json index.html vite.config.ts $(wildcard src/*)
	npm run build

$(MODULE_BINARY): Makefile go.mod *.go cmd/module/*.go $(ENTRYPOINT)
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(MODULE_BINARY) cmd/module/main.go

lint:
	gofmt -s -w .

update:
	go get go.viam.com/rdk@latest
	go get github.com/erh/vmodutils@latest
	go mod tidy

test: $(ENTRYPOINT)
	go test ./...
	npm test

module.tar.gz: meta.json $(MODULE_BINARY) $(ENTRYPOINT)
ifeq ($(VIAM_TARGET_OS), windows)
	jq '.entrypoint = "./bin/capture-control-webapp.exe"' meta.json > temp.json && mv temp.json meta.json
else
	strip $(MODULE_BINARY)
endif
	tar czf $@ --exclude=node_modules meta.json $(MODULE_BINARY) dist
ifeq ($(VIAM_TARGET_OS), windows)
	git checkout meta.json
endif

module: test module.tar.gz

all: test module.tar.gz

setup:
	go mod download
	npm ci

clean:
	rm -rf bin module.tar.gz

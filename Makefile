BINARY := git-pr-failure
GOFLAGS := -trimpath
LDFLAGS := -s -w -extldflags '-static'

.PHONY: build clean vendor

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

clean:
	rm -f $(BINARY)

vendor:
	go mod tidy
	go mod vendor

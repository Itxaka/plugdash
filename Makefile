all: fmt vet test build

build:
	go build -o bin/plugdash ./cmd/plugdash

run:
	go run ./cmd/plugdash

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -rf bin/ *.db

docker:
	docker build -t plugdash .

.PHONY: all build run test vet fmt tidy clean docker

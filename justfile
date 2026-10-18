default:
    @just --list

test:
    go test ./...

vet:
    go vet ./...

check: test vet

build:
    go run ./cmd/build local

dist:
    go run ./cmd/build dist

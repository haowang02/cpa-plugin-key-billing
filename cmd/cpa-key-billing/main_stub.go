//go:build !cshared

// This stub keeps `go build ./...`, `go vet ./...`, and `go test ./...` working
// without cgo. The real entry point lives in main.go behind the cshared tag.
package main

func main() {}

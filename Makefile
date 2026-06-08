.PHONY: frontend build test

# Build the SvelteKit frontend and copy the static output into the Go embed path.
frontend:
	cd web && npm run build
	rm -rf internal/frontend/build
	cp -r web/build internal/frontend/build

# Build the Go binary (run `make frontend` first to update embedded assets).
build: frontend
	go build -o bin/drakkar ./cmd/drakkar

test:
	go test ./...

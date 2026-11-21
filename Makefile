.PHONY: build test vet sim serve demo bottleneck clean

build:
	go build -o bin/eonsa ./cmd/eonsa

test:
	go test ./...

vet:
	go vet ./...

sim:
	go run ./cmd/eonsa sim -out ./out

bottleneck:
	go run ./cmd/eonsa bottleneck -out ./out

demo:
	go run ./cmd/eonsa demo-defrag -out ./out

serve:
	go run ./cmd/eonsa serve -addr :8080

clean:
	rm -rf bin out

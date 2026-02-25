APP=ft

.PHONY: build test run clean

build:
	go build ./cmd/$(APP)


test:
	go test ./...


run:
	go run ./cmd/$(APP)


clean:
	rm -f ./$(APP) ./build.log

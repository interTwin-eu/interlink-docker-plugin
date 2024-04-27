all: sidecars

sidecars:
	CGO_ENABLED=1 GOOS=linux go build -o bin/docker-sd main.go


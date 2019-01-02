.PHONY: build cluster test

default: build

build:
	go build -o bin/swarm    github.com/daccproject/go-dacc/cmd/swarm
	go build -o bin/gdacc     github.com/daccproject/go-dacc/cmd/gdacc
	go build -o bin/bootnode github.com/daccproject/go-dacc/cmd/bootnode

cluster: build
	scripts/boot-cluster.sh

test:
	go test -v github.com/daccproject/go-dacc/swarm/...

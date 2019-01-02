# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: gdacc android ios gdacc-cross swarm evm all test clean
.PHONY: gdacc-linux gdacc-linux-386 gdacc-linux-amd64 gdacc-linux-mips64 gdacc-linux-mips64le
.PHONY: gdacc-linux-arm gdacc-linux-arm-5 gdacc-linux-arm-6 gdacc-linux-arm-7 gdacc-linux-arm64
.PHONY: gdacc-darwin gdacc-darwin-386 gdacc-darwin-amd64
.PHONY: gdacc-windows gdacc-windows-386 gdacc-windows-amd64

GOBIN = $(shell pwd)/build/bin
GO ?= latest

gdacc:
	build/env.sh go run build/ci.go install ./cmd/gdacc
	@echo "Done building."
	@echo "Run \"$(GOBIN)/gdacc\" to launch gdacc."

swarm:
	build/env.sh go run build/ci.go install ./cmd/swarm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/swarm\" to launch swarm."

all:
	build/env.sh go run build/ci.go install

android:
	build/env.sh go run build/ci.go aar --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/gdacc.aar\" to use the library."

ios:
	build/env.sh go run build/ci.go xcode --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/Gdacc.framework\" to use the library."

test: all
	build/env.sh go run build/ci.go test

lint: ## Run linters.
	build/env.sh go run build/ci.go lint

clean:
	./build/clean_go_build_cache.sh
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/kevinburke/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go get -u github.com/golang/protobuf/protoc-gen-go
	env GOBIN= go install ./cmd/abigen
	@type "npm" 2> /dev/null || echo 'Please install node.js and npm'
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

# Cross Compilation Targets (xgo)

gdacc-cross: gdacc-linux gdacc-darwin gdacc-windows gdacc-android gdacc-ios
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-*

gdacc-linux: gdacc-linux-386 gdacc-linux-amd64 gdacc-linux-arm gdacc-linux-mips64 gdacc-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-*

gdacc-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./cmd/gdacc
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep 386

gdacc-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./cmd/gdacc
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep amd64

gdacc-linux-arm: gdacc-linux-arm-5 gdacc-linux-arm-6 gdacc-linux-arm-7 gdacc-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep arm

gdacc-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./cmd/gdacc
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep arm-5

gdacc-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./cmd/gdacc
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep arm-6

gdacc-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./cmd/gdacc
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep arm-7

gdacc-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./cmd/gdacc
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep arm64

gdacc-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./cmd/gdacc
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep mips

gdacc-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./cmd/gdacc
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep mipsle

gdacc-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./cmd/gdacc
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep mips64

gdacc-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./cmd/gdacc
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-linux-* | grep mips64le

gdacc-darwin: gdacc-darwin-386 gdacc-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-darwin-*

gdacc-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./cmd/gdacc
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-darwin-* | grep 386

gdacc-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./cmd/gdacc
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-darwin-* | grep amd64

gdacc-windows: gdacc-windows-386 gdacc-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-windows-*

gdacc-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./cmd/gdacc
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-windows-* | grep 386

gdacc-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./cmd/gdacc
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gdacc-windows-* | grep amd64

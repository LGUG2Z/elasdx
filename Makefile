# ############################################################################## #
# Makefile for Golang Project                                                    #
# Includes cross-compiling, installation, cleanup                                #
# Adapted from https://gist.github.com/cjbarker/5ce66fcca74a1928a155cfb3fea8fac4 #
# ############################################################################## #

# Check for required command tools to build or stop immediately
EXECUTABLES = git go yq kustomize
K := $(foreach exec,$(EXECUTABLES),\
        $(if $(shell which $(exec)),some string,$(error "No $(exec) in PATH)))

ROOT_DIR:=$(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

BINARY=elasdx
VERSION=`cat VERSION`
COMMIT=`git rev-parse HEAD`
PLATFORMS=darwin linux
ARCHITECTURES=amd64

# Setup linker flags option for build that interoperate with variable names in src code
LDFLAGS=-ldflags "-X github.com/LGUG2Z/elasdx/cli.Version=${VERSION} -X github.com/LGUG2Z/elasdx/cli.Commit=${COMMIT}"

default: build

all: clean build_all install

build:
	go build ${LDFLAGS} -o ${BINARY}

build_all:
	$(foreach GOOS, $(PLATFORMS),\
	$(foreach GOARCH, $(ARCHITECTURES), $(shell export GOOS=$(GOOS); export GOARCH=$(GOARCH); go build -v -o $(BINARY)-$(GOOS)-$(GOARCH))))

install:
	go install ${LDFLAGS}

fmt:
	goimports -w cli elasticsearch main.go

release:
	goreleaser --rm-dist

# Remove only what we've created
clean:
	find ${ROOT_DIR} -name '${BINARY}[-?][a-zA-Z0-9]*[-?][a-zA-Z0-9]*' -delete
	rm -rf dist

.PHONY: check clean install build_all all

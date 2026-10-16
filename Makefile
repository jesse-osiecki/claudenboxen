# Build, test, and package claudenboxen.
#
#   make              build ./claudenboxen with the version from git
#   make test         go vet + go test
#   make install      go install into $GOPATH/bin
#   make arch         build the Arch package from packaging/arch/PKGBUILD
#   make arch-install build and install it with makepkg -si
#   make arch-bump VERSION=1.2.3
#                     set pkgver, reset pkgrel, refresh the tarball checksum
#   make clean        remove the binary and makepkg output

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -X github.com/jesse-osiecki/claudenboxen/cmd.version=$(VERSION)
BIN     := claudenboxen
PKGDIR  := packaging/arch

.PHONY: all build install test vet fmt generate arch arch-install arch-checksums arch-bump clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" .

test: vet
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Refresh the pinned Alpine release in internal/image/alpine_pin.go.
generate:
	go generate ./internal/image/

# The PKGBUILD builds the tagged release tarball it pins, not the working
# tree. To package a new release: tag and push it, then `make arch-bump
# VERSION=x.y.z`, commit the PKGBUILD, and `make arch`.
arch:
	cd $(PKGDIR) && makepkg -f

arch-install:
	cd $(PKGDIR) && makepkg -sfi

arch-checksums:
	cd $(PKGDIR) && updpkgsums

arch-bump:
	@test "$(origin VERSION)" = "command line" || { echo "usage: make arch-bump VERSION=x.y.z"; exit 1; }
	sed -i 's/^pkgver=.*/pkgver=$(VERSION)/; s/^pkgrel=.*/pkgrel=1/' $(PKGDIR)/PKGBUILD
	$(MAKE) arch-checksums

clean:
	rm -f $(BIN)
	rm -rf $(PKGDIR)/src $(PKGDIR)/pkg $(PKGDIR)/*.tar.*

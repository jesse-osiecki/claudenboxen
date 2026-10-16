# Build, test, and package claudenboxen.
#
#   make              build ./claudenboxen with the version from git
#   make test         go vet + go test
#   make install      go install into $GOPATH/bin
#   make local        build and install to /usr/local/bin (via sudo), which
#                     shadows the packaged /usr/bin/claudenboxen
#   make arch         build the Arch package from packaging/arch/PKGBUILD
#   make arch-install build and install it with makepkg -si
#   make arch-bump VERSION=1.2.3
#                     set pkgver, reset pkgrel, refresh the tarball checksum
#   make release VERSION=1.2.3
#                     tag, push, arch-bump, commit the PKGBUILD, push again
#   make clean        remove the binary and makepkg output

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -X github.com/jesse-osiecki/claudenboxen/cmd.version=$(VERSION)
BIN     := claudenboxen
PKGDIR  := packaging/arch
PREFIX  ?= /usr/local

.PHONY: all build install local test vet fmt generate arch arch-install arch-checksums arch-bump release clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" .

local: build
	sudo install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)

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
# tree, so a fix isn't in the package until it's in a tagged release.
# `make release VERSION=x.y.z` does the whole sequence: tag HEAD, push the
# tag, point the PKGBUILD at it, commit, push. Then `make arch-install`.
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

release:
	@test "$(origin VERSION)" = "command line" || { echo "usage: make release VERSION=x.y.z"; exit 1; }
	@git diff --quiet HEAD || { echo "working tree has uncommitted changes"; exit 1; }
	git tag -a v$(VERSION) -m "$(BIN) $(VERSION)"
	git push origin main v$(VERSION)
	$(MAKE) arch-bump VERSION=$(VERSION)
	git commit -m "Pin the v$(VERSION) source tarball checksum in the PKGBUILD" $(PKGDIR)/PKGBUILD
	git push origin main

clean:
	rm -f $(BIN)
	rm -rf $(PKGDIR)/src $(PKGDIR)/pkg $(PKGDIR)/*.tar.*

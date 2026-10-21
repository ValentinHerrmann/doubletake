.PHONY: need-go need-gtk all build doubletake doubletake-ctl doubletake-ui doubletake-test-receiver doubletake-release doubletake-ui-release doubletake-ctl-release doubletake-test-receiver-release manpages-release ui-bundle install install-bin install-man install-desktop install-user uninstall uninstall-user test clean

PREFIX ?= /usr/local
DESTDIR ?=
BINDIR ?= $(PREFIX)/bin
DATADIR ?= $(PREFIX)/share
MANDIR ?= $(DATADIR)/man

ARCH := $(shell go env GOARCH 2>/dev/null)
BUNDLE := doubletake-ui-linux-$(ARCH)
RELEASE_FLAGS := -ldflags='-s -w -extldflags=-static'
# doubletake-ui links the system GTK4 (cgo), so it cannot be static.
UI_RELEASE_FLAGS := -ldflags='-s -w'

# Building needs the Go toolchain (go.mod sets the minimum version).
need-go:
	@command -v go >/dev/null || { echo "error: Go is not installed; install it first (Arch: sudo pacman -S go, Debian/Ubuntu: sudo apt install golang-go)" >&2; exit 1; }

# doubletake-ui needs the GTK 4 (4.14 or newer) and GObject Introspection
# development files.
need-gtk:
	@pkg-config --exists 'gtk4 >= 4.14' gobject-introspection-1.0 || { echo "error: GTK 4.14+ or GObject Introspection development files not found (Arch: sudo pacman -S gtk4 gobject-introspection, Debian/Ubuntu: sudo apt install libgtk-4-dev libgirepository1.0-dev)" >&2; exit 1; }

all: doubletake doubletake-ctl doubletake-ui doubletake-test-receiver

build: all

doubletake: need-go
	go build -o bin/doubletake ./cmd/doubletake

doubletake-ctl: need-go
	go build -o bin/doubletake-ctl ./cmd/doubletake-ctl

doubletake-ui: need-go need-gtk
	go build -o bin/doubletake-ui ./cmd/doubletake-ui

doubletake-test-receiver: need-go
	go build -o bin/doubletake-test-receiver ./cmd/doubletake-test-receiver

doubletake-release: need-go
	CGO_ENABLED=0 go build $(RELEASE_FLAGS) -o doubletake ./cmd/doubletake

doubletake-ctl-release: need-go
	CGO_ENABLED=0 go build $(RELEASE_FLAGS) -o doubletake-ctl ./cmd/doubletake-ctl

doubletake-ui-release: need-go need-gtk
	CGO_ENABLED=1 go build $(UI_RELEASE_FLAGS) -o doubletake-ui ./cmd/doubletake-ui

doubletake-test-receiver-release: need-go
	CGO_ENABLED=0 go build $(RELEASE_FLAGS) -o doubletake-test-receiver ./cmd/doubletake-test-receiver

manpages-release:
	tar -czf doubletake-manpages.tar.gz -C man man1

# Desktop app tarball: binaries, desktop entry, icon and an install.sh that
# installs to ~/.local without root. doubletake is static; doubletake-ui needs
# the system GTK 4.
ui-bundle: need-go need-gtk
	rm -rf dist/$(BUNDLE)
	install -d dist/$(BUNDLE)/bin
	CGO_ENABLED=0 go build $(RELEASE_FLAGS) -o dist/$(BUNDLE)/bin/doubletake ./cmd/doubletake
	CGO_ENABLED=1 go build $(UI_RELEASE_FLAGS) -o dist/$(BUNDLE)/bin/doubletake-ui ./cmd/doubletake-ui
	cp -r share dist/$(BUNDLE)/
	install -m 755 packaging/install.sh dist/$(BUNDLE)/
	tar -czf $(BUNDLE).tar.gz -C dist $(BUNDLE)

test: need-go
	go test ./...

# Data files go in only after the binaries built and installed.
install: install-bin
	$(MAKE) install-man install-desktop

install-bin: all
	install -d $(DESTDIR)$(BINDIR)
	install -m 755 bin/doubletake bin/doubletake-ctl bin/doubletake-ui bin/doubletake-test-receiver $(DESTDIR)$(BINDIR)/

install-man:
	install -d $(DESTDIR)$(MANDIR)/man1
	install -m 644 man/man1/doubletake.1 man/man1/doubletake-ctl.1 man/man1/doubletake-test-receiver.1 $(DESTDIR)$(MANDIR)/man1/

# Exec gets the absolute path: desktop sessions often lack ~/.local/bin on PATH.
install-desktop:
	install -d $(DESTDIR)$(DATADIR)/applications $(DESTDIR)$(DATADIR)/icons/hicolor/scalable/apps
	sed 's|^Exec=doubletake-ui|Exec=$(BINDIR)/doubletake-ui|' share/applications/doubletake-ui.desktop > $(DESTDIR)$(DATADIR)/applications/doubletake-ui.desktop
	chmod 644 $(DESTDIR)$(DATADIR)/applications/doubletake-ui.desktop
	install -m 644 share/icons/hicolor/scalable/apps/doubletake.svg $(DESTDIR)$(DATADIR)/icons/hicolor/scalable/apps/
ifeq ($(DESTDIR),)
	if command -v update-desktop-database >/dev/null; then update-desktop-database -q $(DATADIR)/applications || true; fi
	if command -v gtk-update-icon-cache >/dev/null; then gtk-update-icon-cache -q -t $(DATADIR)/icons/hicolor || true; fi
endif

install-user:
	$(MAKE) install PREFIX=$(HOME)/.local

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/doubletake
	rm -f $(DESTDIR)$(BINDIR)/doubletake-ctl
	rm -f $(DESTDIR)$(BINDIR)/doubletake-ui
	rm -f $(DESTDIR)$(BINDIR)/doubletake-test-receiver
	rm -f $(DESTDIR)$(DATADIR)/applications/doubletake-ui.desktop
	rm -f $(DESTDIR)$(DATADIR)/icons/hicolor/scalable/apps/doubletake.svg
	rm -f $(DESTDIR)$(MANDIR)/man1/doubletake.1
	rm -f $(DESTDIR)$(MANDIR)/man1/doubletake-ctl.1
	rm -f $(DESTDIR)$(MANDIR)/man1/doubletake-test-receiver.1

uninstall-user:
	$(MAKE) uninstall PREFIX=$(HOME)/.local

clean:
	rm -rf bin/ dist/ doubletake-ui-linux-*.tar.gz
	go clean -testcache

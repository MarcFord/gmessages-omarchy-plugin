PREFIX      ?= $(HOME)/.local
BINDIR      ?= $(PREFIX)/bin
PLUGIN_ID   := marcford.gmessages
PLUGIN_DIR  ?= $(HOME)/.config/omarchy/plugins/$(PLUGIN_ID)
UNIT_DIR    ?= $(HOME)/.config/systemd/user
DATA_DIR    ?= $(if $(XDG_DATA_HOME),$(XDG_DATA_HOME),$(HOME)/.local/share)/gmessages-omarchy
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

QML_FILES   := Widget.qml Panel.qml GmClient.qml Avatar.qml Model.js manifest.json

.PHONY: all build test lint install install-daemon install-plugin install-service uninstall clean

all: build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/gmessagesd ./cmd/gmessagesd

test:
	go test ./...

lint:
	go vet ./...
	@command -v qmllint >/dev/null && qmllint $(filter %.qml,$(QML_FILES)) || echo "qmllint not installed, skipping QML lint"

install: install-daemon install-plugin install-service
	@echo
	@echo "Installed. Next:"
	@echo "  systemctl --user enable --now gmessagesd"
	@echo "  omarchy plugin enable $(PLUGIN_ID)"

install-daemon: build
	install -Dm755 bin/gmessagesd $(BINDIR)/gmessagesd

install-plugin:
	install -d $(PLUGIN_DIR)
	install -m644 $(QML_FILES) $(PLUGIN_DIR)/
	@command -v omarchy-shell >/dev/null && omarchy-shell shell rescanPlugins || true

install-service:
	install -Dm644 systemd/gmessagesd.service $(UNIT_DIR)/gmessagesd.service
	# ProtectHome=read-only means the unit's ReadWritePaths= entry must already
	# exist, and it is resolved before ExecStart runs -- so the daemon cannot
	# create its own data directory. systemd creates the runtime and cache dirs.
	install -d -m700 $(DATA_DIR)
	systemctl --user daemon-reload

uninstall:
	-systemctl --user disable --now gmessagesd
	rm -f $(BINDIR)/gmessagesd $(UNIT_DIR)/gmessagesd.service
	rm -rf $(PLUGIN_DIR)
	systemctl --user daemon-reload

clean:
	rm -rf bin

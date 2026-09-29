# The whole dev loop is `make`. See scripts/dev.sh for what each target does.
.PHONY: dev build shell console log reset test go-test status destroy refresh release clean

# Build if anything changed, then install or update the dev VM to that build.
dev:
	@./scripts/dev.sh dev

# Build out/ only (skipped when nothing changed).
build:
	@./scripts/dev.sh build

# Serial shell in the dev VM (Ctrl-O to leave).
shell:
	@./scripts/dev.sh shell

# Reopen the VM's display in Screen Sharing.
console:
	@./scripts/dev.sh console

# Follow the VM's serial console.
log:
	@./scripts/dev.sh log

# Wipe the dev VM and install it from scratch.
reset:
	@./scripts/dev.sh reset

# Reinstall the dev VM from scratch through the web installer and check it end
# to end: API, hardening, update, rollback and the health-check fallback.
test:
	@./scripts/dev.sh test

# Vet and unit-test the Go code, here on this machine (no VM, no builder).
go-test:
	go vet ./...
	go test ./...

status:
	@./scripts/dev.sh status

destroy:
	@./scripts/dev.sh destroy

# Rebuild the package base with the newest Arch packages.
refresh:
	REFRESH=1 ./scripts/build.sh

# Smaller image, slower compression.
release:
	COMPRESS=lzma,level=6 ./scripts/build.sh

clean:
	rm -rf out
	./scripts/build.sh clean

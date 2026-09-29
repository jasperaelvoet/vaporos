# The whole dev loop is `make`. See scripts/dev.sh for what each target does.
.PHONY: dev build shell console log reset test status destroy refresh release clean

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

# End-to-end test on a separate throwaway VM; the dev VM is left alone.
test:
	@./scripts/dev.sh test

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
	docker volume rm -f vos-work wvos-work wvos-pkgcache >/dev/null

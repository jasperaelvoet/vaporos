.PHONY: image iso packages dev dev-destroy all clean

all: iso

image:
	./scripts/build-image.sh

iso: image
	./scripts/build-iso.sh

clean:
	rm -rf out

packages:
	docker build --platform linux/amd64 -f Containerfile.packages \
		-t localhost/watervaporos-aur-packages:latest .

# Boot the ISO in a throwaway VM on the Proxmox host and open its console.
dev:
	./scripts/dev-vm.sh fetch
	./scripts/dev-vm.sh up

dev-destroy:
	./scripts/dev-vm.sh destroy

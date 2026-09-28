.PHONY: image iso packages all clean

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

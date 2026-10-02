package store

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fsverityDigest is the kernel's struct fsverity_digest with room for the
// largest digest (sha512).
type fsverityDigest struct {
	Alg    uint16
	Size   uint16
	Digest [64]byte
}

// control runs fn on f's descriptor, keeping f alive and in its poller's
// mode (os.File.Fd would switch it to blocking).
func control(f *os.File, fn func(fd uintptr) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(fd) }); err != nil {
		return err
	}
	return ferr
}

func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg))
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

// unsupported maps the errors a filesystem without fs-verity gives.
func unsupported(err error) error {
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return err
}

func sysEnableVerity(f *os.File) error {
	arg := unix.FsverityEnableArg{
		Version:        1,
		Hash_algorithm: unix.FS_VERITY_HASH_ALG_SHA256,
		Block_size:     4096,
	}
	return unsupported(control(f, func(fd uintptr) error {
		return ioctl(fd, unix.FS_IOC_ENABLE_VERITY, unsafe.Pointer(&arg))
	}))
}

func sysMeasureVerity(f *os.File) (string, error) {
	d := fsverityDigest{Size: uint16(len(fsverityDigest{}.Digest))}
	err := control(f, func(fd uintptr) error {
		return ioctl(fd, unix.FS_IOC_MEASURE_VERITY, unsafe.Pointer(&d))
	})
	if errors.Is(err, unix.ENODATA) {
		return "", errNotSealed
	}
	if err != nil {
		return "", unsupported(err)
	}
	if d.Alg != unix.FS_VERITY_HASH_ALG_SHA256 || d.Size != 32 {
		return "", fmt.Errorf("%w: hash algorithm %d", errNotSealed, d.Alg)
	}
	return hex.EncodeToString(d.Digest[:32]), nil
}

func sysVerityAttr(f *os.File) (bool, error) {
	var st unix.Statx_t
	err := control(f, func(fd uintptr) error {
		return unix.Statx(int(fd), "", unix.AT_EMPTY_PATH, unix.STATX_TYPE|unix.STATX_SIZE, &st)
	})
	if err != nil {
		return false, err
	}
	return st.Attributes&unix.STATX_ATTR_VERITY != 0, nil
}

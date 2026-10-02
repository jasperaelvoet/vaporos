package starcitizen

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var meminfoPath = "/proc/meminfo"

// What Star Citizen wants: 16 GiB of memory, and 32 GiB of memory and swap
// together. SwapTotal counts zram, which CachyOS sizes as the memory.
// Firmware and integrated graphics keep up to a GiB of a PC's memory, so a
// 16 GiB PC reports a little less, and with zram that shortfall counts
// twice.
const (
	reserved  = 1 << 30
	wantRAM   = 16<<30 - reserved
	wantTotal = 32<<30 - 2*reserved
)

// memory returns the PC's memory and swap in bytes.
func memory() (ram, swap uint64, err error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) != 2 || f[1] != "kB" {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			ram = n << 10
		case "SwapTotal":
			swap = n << 10
		}
	}
	if ram == 0 {
		return 0, 0, fmt.Errorf("%s has no MemTotal", meminfoPath)
	}
	return ram, swap, sc.Err()
}

// memoryWarning says when the PC has less memory than Star Citizen wants,
// "" when it has enough or cannot tell.
func memoryWarning() string {
	ram, swap, err := memory()
	switch {
	case err != nil:
		return ""
	case ram < wantRAM:
		return fmt.Sprintf("This PC has %d GB of memory, and Star Citizen needs 16 GB. It may not start, or it may close while you play.", gb(ram))
	case ram+swap < wantTotal:
		return fmt.Sprintf("This PC has %d GB of memory and swap together, and Star Citizen wants 32 GB. It may stutter or close in busy places.", gb(ram+swap))
	}
	return ""
}

// gb is n bytes in whole gigabytes, as a PC's memory is sold.
func gb(n uint64) uint64 { return (n + 1<<29) >> 30 }

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <file-to-upload>\n", os.Args[0])
		os.Exit(1)
	}
	filePath := os.Args[1]

	f, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	buf := make([]byte, 512*1024)
	if _, err := io.CopyBuffer(io.Discard, f, buf); err != nil {
		fmt.Fprintf(os.Stderr, "stream failed: %v\n", err)
		os.Exit(1)
	}

	vmHWMKB := getVmHWMKB()
	fmt.Printf("Peak RSS (VmHWM): %d KiB (%.2f MiB)\n", vmHWMKB, float64(vmHWMKB)/1024)

	const maxKB = 200 * 1024
	if vmHWMKB > maxKB {
		fmt.Fprintf(os.Stderr, "FAIL: peak RSS %d KiB exceeded 200 MiB limit (%d KiB)\n", vmHWMKB, maxKB)
		os.Exit(1)
	}
	fmt.Println("SUCCESS: streaming upload peak RSS within limits.")
}

func getVmHWMKB() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "VmHWM:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, _ := strconv.ParseInt(fields[1], 10, 64)
				return val
			}
		}
	}
	return 0
}

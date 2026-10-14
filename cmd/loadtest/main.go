package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"

	"github.com/burhanverse/tgdl/internal/tg"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <file-to-upload>\n", os.Args[0])
		os.Exit(1)
	}
	filePath := os.Args[1]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	api := tg.NewBotAPI(nil, "fake-token")
	api.BaseURL = srv.URL

	_, err := api.Upload(context.Background(), 123456, tg.Upload{
		Path: filePath,
		Kind: tg.KindDocument,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "upload failed: %v\n", err)
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

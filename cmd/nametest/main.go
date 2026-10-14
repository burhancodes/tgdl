package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type tgDocResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int `json:"message_id"`
		Document  struct {
			FileID   string `json:"file_id"`
			FileName string `json:"file_name"`
			FileSize int64  `json:"file_size"`
		} `json:"document"`
	} `json:"result,omitempty"`
}

func main() {
	tokenFlag := flag.String("token", "", "Telegram bot token")
	chatFlag := flag.Int64("chat", 0, "Test chat ID")
	apiFlag := flag.String("api", "https://api.telegram.org", "Telegram Bot API base URL")
	flag.Parse()

	token := *tokenFlag
	if token == "" {
		token = os.Getenv("TG_BOT_TOKEN")
	}
	chatID := *chatFlag
	if chatID == 0 {
		if s := os.Getenv("TG_TEST_CHAT_ID"); s != "" {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				chatID = id
			}
		}
	}
	baseURL := strings.TrimRight(*apiFlag, "/")
	if u := os.Getenv("TG_BOT_API_URL"); u != "" && *apiFlag == "https://api.telegram.org" {
		baseURL = strings.TrimRight(u, "/")
	}

	if token == "" || chatID == 0 {
		fmt.Println("Usage: nametest -token <BOT_TOKEN> -chat <CHAT_ID> [-api <API_URL>]")
		fmt.Println("Or set TG_BOT_TOKEN and TG_TEST_CHAT_ID in environment.")
		os.Exit(1)
	}

	lengths := []int{100, 255, 256, 400, 600, 1000}

	fmt.Println("| Type | Target Chars | Sent Filename Length (chars / bytes) | Recv document.file_name (chars / bytes) | Match? | Status / Note |")
	fmt.Println("| :--- | :--- | :--- | :--- | :--- | :--- |")

	dummyContent := bytes.Repeat([]byte("A"), 1024)

	for _, l := range lengths {
		testASCII := makeName("test_ascii_", ".bin", l, "x")
		testUpload(baseURL, token, chatID, "ASCII", l, testASCII, dummyContent)
		time.Sleep(1 * time.Second)
	}

	for _, l := range lengths {
		testUnicode := makeName("テスト_絵文字_🔥_", ".bin", l, "日")
		testUpload(baseURL, token, chatID, "Japanese/Emoji", l, testUnicode, dummyContent)
		time.Sleep(1 * time.Second)
	}
}

func makeName(prefix, ext string, targetChars int, pad string) string {
	needed := targetChars - len([]rune(prefix)) - len([]rune(ext))
	if needed <= 0 {
		return prefix + ext
	}
	var sb strings.Builder
	sb.WriteString(prefix)
	padRune := []rune(pad)[0]
	for i := 0; i < needed; i++ {
		sb.WriteRune(padRune)
	}
	sb.WriteString(ext)
	return sb.String()
}

func testUpload(baseURL, token string, chatID int64, kind string, targetLen int, filename string, content []byte) {
	sentRunes := len([]rune(filename))
	sentBytes := len([]byte(filename))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	part, err := mw.CreateFormFile("document", filename)
	if err != nil {
		fmt.Printf("| %s | %d | %d / %d | ERR | NO | form error: %v |\n", kind, targetLen, sentRunes, sentBytes, err)
		return
	}
	_, _ = part.Write(content)
	_ = mw.Close()

	endpoint := fmt.Sprintf("%s/bot%s/sendDocument", baseURL, token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		fmt.Printf("| %s | %d | %d / %d | ERR | NO | req error: %v |\n", kind, targetLen, sentRunes, sentBytes, err)
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("| %s | %d | %d / %d | ERR | NO | do error: %v |\n", kind, targetLen, sentRunes, sentBytes, err)
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tgResp tgDocResponse
	if err := json.Unmarshal(respBytes, &tgResp); err != nil {
		fmt.Printf("| %s | %d | %d / %d | ERR | NO | unmarshal: %v |\n", kind, targetLen, sentRunes, sentBytes, err)
		return
	}

	if !tgResp.OK {
		fmt.Printf("| %s | %d | %d / %d | - | NO | Rejected: %d %s |\n", kind, targetLen, sentRunes, sentBytes, tgResp.ErrorCode, tgResp.Description)
		return
	}

	msgID := tgResp.Result.MessageID
	if msgID != 0 {
		delEndpoint := fmt.Sprintf("%s/bot%s/deleteMessage", baseURL, token)
		delBody, _ := json.Marshal(map[string]any{"chat_id": chatID, "message_id": msgID})
		delReq, _ := http.NewRequest(http.MethodPost, delEndpoint, bytes.NewReader(delBody))
		delReq.Header.Set("Content-Type", "application/json")
		if delResp, err := http.DefaultClient.Do(delReq); err == nil {
			_ = delResp.Body.Close()
		}
	}

	recv := tgResp.Result.Document.FileName
	recvRunes := len([]rune(recv))
	recvBytes := len([]byte(recv))
	matched := "YES"
	note := "Preserved exact name"
	if recv != filename {
		matched = "NO"
		note = fmt.Sprintf("Truncated/altered to %d chars (%s)", recvRunes, recv)
	}

	fmt.Printf("| %s | %d | %d / %d | %d / %d | %s | %s |\n", kind, targetLen, sentRunes, sentBytes, recvRunes, recvBytes, matched, note)
}

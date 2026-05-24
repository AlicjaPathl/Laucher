package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"
)

var serverURL = "http://127.0.0.1:8080"
var wsURL = "ws://127.0.0.1:8080/ws"
var tcpAddr = "127.0.0.1:8082"

var wsConn *websocket.Conn
var currentUser string

type authRequest struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ChatMsg struct {
	Username string `json:"username"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

type PMMsg struct {
	FromUser string `json:"from_user"`
	ToUser   string `json:"to_user"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

type ctfSubmitRequest struct {
	Username string `json:"username"`
	TaskID   string `json:"task_id"`
	FlagHash string `json:"flag_hash"` // Wysyłamy hash SHA-512, żeby nikt nie podsłuchał flagi w czystym tekście
}

func apiPost(endpoint string, payload interface{}) (map[string]string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(serverURL+endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("brak połączenia z serwerem")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var result map[string]string
	json.Unmarshal(body, &result)
	return result, nil
}

// hashFlag zamienia tekst flagi (np. CTF{secret}) na ciąg znaków SHA-512 hex
func hashFlag(flag string) string {
	hash := sha512.Sum512([]byte(strings.TrimSpace(flag)))
	return fmt.Sprintf("%x", hash)
}

// Funkcje pomocnicze dla pobierania plików gier (z zachowaniem Twojej logiki TCP)
func getLocalFileMD5(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func tcpFetchFileList(gameID string) ([]string, error) {
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "FILES %s\n", gameID)
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "FILES ") {
		return nil, fmt.Errorf("bad response")
	}
	filesStr := strings.TrimPrefix(line, "FILES ")
	if filesStr == "" {
		return []string{}, nil
	}
	return strings.Split(filesStr, ","), nil
}

func tcpDownloadSingleFile(gameID, remotePath, localFullPath string) error {
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "DOWNLOAD %s/%s\n", gameID, remotePath)
	reader := bufio.NewReader(conn)
	respLine, _ := reader.ReadString('\n')
	respLine = strings.TrimRight(respLine, "\r\n")
	parts := strings.SplitN(respLine, " ", 2)
	var fileSize int64
	fmt.Sscanf(parts[1], "%d", &fileSize)

	os.MkdirAll(filepath.Dir(localFullPath), 0755)
	f, err := os.Create(localFullPath)
	if err != nil {
		return err
	}
	io.CopyN(f, reader, fileSize)
	f.Close()
	os.Chmod(localFullPath, 0755)
	return nil
}

package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

var db *sql.DB

// Token admina – ustaw przez zmienną środowiskową ADMIN_TOKEN
var adminToken = func() string {
	t := os.Getenv("ADMIN_TOKEN")
	if t == "" {
		t = "neon-admin-secret"
	}
	return t
}()

const gamesDir = "./games" // games/<name>/<pliki>

func initDB() {
	var err error
	connStr := "user=postgres password=postgres dbname=postgres sslmode=disable"
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("Blad konfiguracji bazy: %v", err)
		return
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY, email VARCHAR(255) UNIQUE NOT NULL,
		username VARCHAR(50) UNIQUE NOT NULL, password_hash VARCHAR(255) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, last_login TIMESTAMP,
		is_banned BOOLEAN DEFAULT FALSE, ban_reason TEXT, ban_until TIMESTAMP,
		role VARCHAR(20) DEFAULT 'user', display_name VARCHAR(50), avatar_url TEXT,
		country VARCHAR(2), elo INTEGER DEFAULT 1000, level INTEGER DEFAULT 1,
		xp INTEGER DEFAULT 0, is_verified BOOLEAN DEFAULT FALSE,
		email_verified BOOLEAN DEFAULT FALSE, last_ip VARCHAR(45), last_device TEXT
	);`)
	db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id SERIAL PRIMARY KEY, username VARCHAR(50) NOT NULL,
		content TEXT NOT NULL, sent_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`)
	db.Exec(`CREATE TABLE IF NOT EXISTS private_messages (
		id SERIAL PRIMARY KEY, from_user VARCHAR(50) NOT NULL,
		to_user VARCHAR(50) NOT NULL, content TEXT NOT NULL,
		sent_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, is_read BOOLEAN DEFAULT FALSE
	);`)
	db.Exec(`CREATE TABLE IF NOT EXISTS games (
		id SERIAL PRIMARY KEY,
		name VARCHAR(100) UNIQUE NOT NULL,
		display_name VARCHAR(200) NOT NULL,
		description TEXT,
		version VARCHAR(50) DEFAULT '1.0.0',
		size_bytes BIGINT DEFAULT 0,
		main_exe_windows VARCHAR(500) DEFAULT '',
		main_exe_linux VARCHAR(500) DEFAULT '',
		added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`)
	// Migracja – dodaj kolumny jeśli nie istnieją (dla istniejących baz)
	db.Exec(`ALTER TABLE games ADD COLUMN IF NOT EXISTS main_exe_windows VARCHAR(500) DEFAULT ''`)
	db.Exec(`ALTER TABLE games ADD COLUMN IF NOT EXISTS main_exe_linux VARCHAR(500) DEFAULT ''`)
	db.Exec(`ALTER TABLE games ADD COLUMN IF NOT EXISTS category VARCHAR(50) DEFAULT 'game'`)
	db.Exec(`ALTER TABLE games ADD COLUMN IF NOT EXISTS upvotes INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE games ADD COLUMN IF NOT EXISTS downvotes INTEGER DEFAULT 0`)

	db.Exec(`CREATE TABLE IF NOT EXISTS comments (
		id SERIAL PRIMARY KEY,
		game_name VARCHAR(100) NOT NULL,
		username VARCHAR(50) NOT NULL,
		content TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`)
	db.Exec(`CREATE TABLE IF NOT EXISTS game_reactions (
		id SERIAL PRIMARY KEY,
		game_name VARCHAR(100) NOT NULL,
		username VARCHAR(50) NOT NULL,
		is_upvote BOOLEAN NOT NULL,
		UNIQUE(game_name, username)
	);`)
	db.Exec(`CREATE TABLE IF NOT EXISTS user_reposts (
		id SERIAL PRIMARY KEY,
		username VARCHAR(50) NOT NULL,
		game_name VARCHAR(100) NOT NULL,
		reposted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(username, game_name)
	);`)

	log.Println("Baza danych gotowa!")
	os.MkdirAll(gamesDir, 0755)
}

// --- CORS ---
func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
}

// sanitizeGameName zamienia spacje i znaki specjalne na '_' i zwraca bezpieczna nazwe katalogu
func sanitizeGameName(name string) string {
	name = strings.TrimSpace(name)
	var out []rune
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	s := strings.Trim(string(out), "_.")
	if s == "" {
		return "unnamed"
	}
	return s
}

func checkAdmin(r *http.Request) bool {
	return r.Header.Get("X-Admin-Token") == adminToken
}

// --- Auth ---
type AuthRequest struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", 400)
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	_, err := db.Exec("INSERT INTO users (email, username, password_hash) VALUES ($1,$2,$3)", req.Email, req.Username, string(hash))
	if err != nil {
		http.Error(w, "Email lub nazwa zajete.", 409)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", 400)
		return
	}
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE username=$1", req.Username).Scan(&hash); err != nil {
		http.Error(w, "Bledny login lub haslo", 401)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		http.Error(w, "Bledny login lub haslo", 401)
		return
	}
	db.Exec("UPDATE users SET last_login=$1 WHERE username=$2", time.Now(), req.Username)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "username": req.Username})
}

// --- Chat history ---
type ChatMessage struct {
	Username string `json:"username"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

func messagesHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	rows, _ := db.Query("SELECT username, content, sent_at FROM messages ORDER BY sent_at DESC LIMIT 50")
	defer rows.Close()
	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var t time.Time
		rows.Scan(&m.Username, &m.Content, &t)
		m.SentAt = t.Format("15:04")
		msgs = append(msgs, m)
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	if msgs == nil {
		msgs = []ChatMessage{}
	}
	json.NewEncoder(w).Encode(msgs)
}

// --- Private Messages ---
func pmHistoryHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	rows, err := db.Query(`SELECT from_user,to_user,content,sent_at FROM private_messages
		WHERE (from_user=$1 AND to_user=$2) OR (from_user=$2 AND to_user=$1)
		ORDER BY sent_at ASC LIMIT 100`, from, to)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer rows.Close()
	type PMMsg struct {
		FromUser string `json:"from_user"`
		ToUser   string `json:"to_user"`
		Content  string `json:"content"`
		SentAt   string `json:"sent_at"`
	}
	var msgs []PMMsg
	for rows.Next() {
		var m PMMsg
		var t time.Time
		rows.Scan(&m.FromUser, &m.ToUser, &m.Content, &t)
		m.SentAt = t.Format("15:04")
		msgs = append(msgs, m)
	}
	if msgs == nil {
		msgs = []PMMsg{}
	}
	json.NewEncoder(w).Encode(msgs)
}

func pmConversationsHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	user := r.URL.Query().Get("user")
	rows, err := db.Query(`SELECT DISTINCT CASE WHEN from_user=$1 THEN to_user ELSE from_user END AS partner
		FROM private_messages WHERE from_user=$1 OR to_user=$1`, user)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer rows.Close()
	var partners []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		partners = append(partners, p)
	}
	if partners == nil {
		partners = []string{}
	}
	json.NewEncoder(w).Encode(partners)
}

// =============================================
// ADMIN API (chroniony przez X-Admin-Token)
// =============================================

type GameInfo struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	Version        string   `json:"version"`
	SizeBytes      int64    `json:"size_bytes"`
	MainExeWindows string   `json:"main_exe_windows"`
	MainExeLinux   string   `json:"main_exe_linux"`
	Files          []string `json:"files,omitempty"`
	Category       string   `json:"category"`
	Upvotes        int      `json:"upvotes"`
	Downvotes      int      `json:"downvotes"`
}

func adminGamesListHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}

	entries, _ := os.ReadDir(gamesDir)
	var games []GameInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		var g GameInfo
		db.QueryRow("SELECT name,display_name,description,version,size_bytes,COALESCE(main_exe_windows,''),COALESCE(main_exe_linux,''),COALESCE(category,'game'),upvotes,downvotes FROM games WHERE name=$1", name).
			Scan(&g.Name, &g.DisplayName, &g.Description, &g.Version, &g.SizeBytes, &g.MainExeWindows, &g.MainExeLinux, &g.Category, &g.Upvotes, &g.Downvotes)
		if g.Name == "" {
			g.Name = name
			g.DisplayName = name
		}

		// Policz pliki
		var totalSize int64
		filepath.WalkDir(filepath.Join(gamesDir, name), func(path string, d os.DirEntry, _ error) error {
			if !d.IsDir() {
				info, _ := d.Info()
				totalSize += info.Size()
				rel, _ := filepath.Rel(filepath.Join(gamesDir, name), path)
				g.Files = append(g.Files, rel)
			}
			return nil
		})
		g.SizeBytes = totalSize
		games = append(games, g)
	}
	if games == nil {
		games = []GameInfo{}
	}
	json.NewEncoder(w).Encode(games)
}

func adminUploadHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}

	gameName := sanitizeGameName(r.URL.Query().Get("game"))
	fileName := r.URL.Query().Get("file")
	if gameName == "" || fileName == "" {
		http.Error(w, "missing ?game=&file=", 400)
		return
	}

	// Bezpieczne czyszczenie sciezki pliku
	fileName = filepath.Clean(fileName)
	if strings.HasPrefix(fileName, "..") || strings.HasPrefix(fileName, "/") {
		http.Error(w, "Invalid path traversal attempt", 400)
		return
	}

	destDir := filepath.Join(gamesDir, gameName)
	destPath := filepath.Join(destDir, fileName)

	// Utworz podkatalogi, jesli plik jest w podkatalogu
	os.MkdirAll(filepath.Dir(destPath), 0755)

	f, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "Nie można zapisać pliku: "+err.Error(), 500)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, "Błąd uploadu", 500)
		return
	}

	// Uaktualnij bazę o calkowity rozmiar
	db.Exec(`INSERT INTO games (name, display_name, size_bytes, updated_at)
		VALUES ($1,$1,$2,NOW())
		ON CONFLICT (name) DO UPDATE SET size_bytes=size_bytes+$2, updated_at=NOW()`,
		gameName, n)

	log.Printf("[Admin] Upload: %s/%s (%.1f MB)", gameName, fileName, float64(n)/1024/1024)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "file": destPath})
}

func adminDeleteHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}
	gameName := sanitizeGameName(r.URL.Query().Get("game"))
	if gameName == "" || gameName == "." {
		http.Error(w, "missing ?game=", 400)
		return
	}
	os.RemoveAll(filepath.Join(gamesDir, gameName))
	db.Exec("DELETE FROM games WHERE name=$1", gameName)
	log.Printf("[Admin] Usunieto gre: %s", gameName)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func adminMetaHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}
	var meta struct {
		Name           string `json:"name"`
		DisplayName    string `json:"display_name"`
		Description    string `json:"description"`
		Version        string `json:"version"`
		MainExeWindows string `json:"main_exe_windows"`
		MainExeLinux   string `json:"main_exe_linux"`
		Category       string `json:"category"`
	}
	json.NewDecoder(r.Body).Decode(&meta)

	if meta.Category == "" {
		meta.Category = "game"
	}

	gameName := sanitizeGameName(meta.Name)
	if gameName == "" {
		http.Error(w, "Brak nazwy gry", 400)
		return
	}
	meta.Name = gameName

	os.MkdirAll(filepath.Join(gamesDir, gameName), 0755)

	_, err := db.Exec(`INSERT INTO games (name, display_name, description, version, main_exe_windows, main_exe_linux, category)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (name) DO UPDATE SET display_name=$2, description=$3, version=$4, main_exe_windows=$5, main_exe_linux=$6, category=$7, updated_at=NOW()`,
		gameName, meta.DisplayName, meta.Description, meta.Version, meta.MainExeWindows, meta.MainExeLinux, meta.Category)
	if err != nil {
		log.Printf("[Admin Error] SaveMeta failed: %v", err)
		http.Error(w, "Blad bazy: "+err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// =============================================
// NOWE ENDPOINTY – zarządzanie pojedynczymi plikami
// =============================================

// DELETE /admin/games/file?game=xxx&file=linux/run.exe
func adminFileDeleteHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}

	gameName := sanitizeGameName(r.URL.Query().Get("game"))
	filePath := r.URL.Query().Get("file")

	if gameName == "" || filePath == "" {
		http.Error(w, "missing game or file parameter", 400)
		return
	}

	filePath = filepath.Clean(filePath)
	if strings.HasPrefix(filePath, "..") || filepath.IsAbs(filePath) {
		http.Error(w, "invalid file path", 400)
		return
	}

	fullPath := filepath.Join(gamesDir, gameName, filePath)

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "file not found", 404)
		return
	}

	if err := os.Remove(fullPath); err != nil {
		http.Error(w, fmt.Sprintf("delete failed: %v", err), 500)
		return
	}

	// Posprzątaj puste katalogi
	dir := filepath.Dir(fullPath)
	for dir != filepath.Join(gamesDir, gameName) {
		if entries, _ := os.ReadDir(dir); len(entries) == 0 {
			os.Remove(dir)
			dir = filepath.Dir(dir)
		} else {
			break
		}
	}

	log.Printf("[Admin] Usunieto plik: %s/%s", gameName, filePath)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "deleted": filePath})
}

// POST /admin/games/move?game=xxx&file=linux/run.exe&platform=windows
func adminFileMoveHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	if !checkAdmin(r) {
		http.Error(w, "Unauthorized", 401)
		return
	}

	gameName := sanitizeGameName(r.URL.Query().Get("game"))
	oldPath := r.URL.Query().Get("file")
	newPlatform := r.URL.Query().Get("platform") // "windows", "linux", "" = common

	if gameName == "" || oldPath == "" {
		http.Error(w, "missing parameters", 400)
		return
	}

	oldPath = filepath.Clean(oldPath)
	if strings.HasPrefix(oldPath, "..") || filepath.IsAbs(oldPath) {
		http.Error(w, "invalid path", 400)
		return
	}

	parts := strings.SplitN(filepath.ToSlash(oldPath), "/", 2)
	var newPath string
	if newPlatform == "" { // common
		if len(parts) > 1 {
			newPath = parts[1]
		} else {
			newPath = oldPath
		}
	} else {
		prefix := newPlatform + "/"
		if len(parts) > 1 {
			newPath = prefix + parts[1]
		} else {
			newPath = prefix + oldPath
		}
	}

	oldFull := filepath.Join(gamesDir, gameName, oldPath)
	newFull := filepath.Join(gamesDir, gameName, newPath)

	if _, err := os.Stat(oldFull); os.IsNotExist(err) {
		http.Error(w, "source file not found", 404)
		return
	}
	if _, err := os.Stat(newFull); err == nil {
		http.Error(w, "destination file already exists", 409)
		return
	}

	if err := os.MkdirAll(filepath.Dir(newFull), 0755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create directory: %v", err), 500)
		return
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		http.Error(w, fmt.Sprintf("move failed: %v", err), 500)
		return
	}

	// Posprzątaj stary katalog
	oldDir := filepath.Dir(oldFull)
	for oldDir != filepath.Join(gamesDir, gameName) {
		if entries, _ := os.ReadDir(oldDir); len(entries) == 0 {
			os.Remove(oldDir)
			oldDir = filepath.Dir(oldDir)
		} else {
			break
		}
	}

	log.Printf("[Admin] Przeniesiono plik: %s/%s -> %s/%s", gameName, oldPath, gameName, newPath)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"old_path": oldPath,
		"new_path": newPath,
	})
}

// =============================================
// PUBLICZNE API – lista gier dla launchera
// =============================================
func gamesListHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	rows, _ := db.Query("SELECT name, display_name, description, version, size_bytes, COALESCE(main_exe_windows,''), COALESCE(main_exe_linux,''), COALESCE(category,'game'), upvotes, downvotes FROM games ORDER BY name")
	defer rows.Close()
	var games []GameInfo
	for rows.Next() {
		var g GameInfo
		rows.Scan(&g.Name, &g.DisplayName, &g.Description, &g.Version, &g.SizeBytes, &g.MainExeWindows, &g.MainExeLinux, &g.Category, &g.Upvotes, &g.Downvotes)
		games = append(games, g)
	}
	if games == nil {
		games = []GameInfo{}
	}
	json.NewEncoder(w).Encode(games)
}

// =============================================
// WEBSOCKET – czat globalny i prywatny
// =============================================
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

type WSClient struct {
	conn     *websocket.Conn
	username string
}

var wsClients = make(map[string]*WSClient)
var wsMu sync.Mutex

func chatHandler(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("user")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	if username != "" {
		wsMu.Lock()
		wsClients[username] = &WSClient{conn, username}
		wsMu.Unlock()
	}
	defer func() { wsMu.Lock(); delete(wsClients, username); wsMu.Unlock() }()

	for {
		_, p, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg := string(p)
		if strings.HasPrefix(msg, "PM:") {
			parts := strings.SplitN(msg, ":", 4)
			if len(parts) == 4 {
				from, to, content := parts[1], parts[2], parts[3]
				db.Exec("INSERT INTO private_messages (from_user,to_user,content) VALUES ($1,$2,$3)", from, to, content)
				wsMu.Lock()
				if c, ok := wsClients[to]; ok {
					c.conn.WriteMessage(websocket.TextMessage, p)
				}
				if c, ok := wsClients[from]; ok {
					c.conn.WriteMessage(websocket.TextMessage, p)
				}
				wsMu.Unlock()
			}
		} else if strings.HasPrefix(msg, "GLOBAL:") {
			parts := strings.SplitN(msg, ":", 3)
			if len(parts) == 3 {
				db.Exec("INSERT INTO messages (username,content) VALUES ($1,$2)", parts[1], parts[2])
			}
			wsMu.Lock()
			for _, c := range wsClients {
				c.conn.WriteMessage(websocket.TextMessage, p)
			}
			wsMu.Unlock()
		}
	}
}

// =============================================
// SERWER TCP (port 8082) – pobieranie gier
// =============================================
func handleTCPClient(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 2)

		switch parts[0] {
		case "LIST":
			entries, _ := os.ReadDir(gamesDir)
			var names []string
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			fmt.Fprintf(conn, "GAMES %s\n", strings.Join(names, ","))

		case "FILES":
			if len(parts) < 2 {
				fmt.Fprintf(conn, "ERROR brak nazwy\n")
				continue
			}
			gameName := sanitizeGameName(parts[1])
			gameDir := filepath.Join(gamesDir, gameName)
			var files []string
			filepath.WalkDir(gameDir, func(path string, d os.DirEntry, _ error) error {
				if !d.IsDir() {
					rel, _ := filepath.Rel(gameDir, path)
					info, err := d.Info()
					if err == nil {
						size := info.Size()
						md5Hash := ""
						f, errOpen := os.Open(path)
						if errOpen == nil {
							h := md5.New()
							if _, errCopy := io.Copy(h, f); errCopy == nil {
								md5Hash = fmt.Sprintf("%x", h.Sum(nil))
							}
							f.Close()
						}
						files = append(files, fmt.Sprintf("%s:%d:%s", rel, size, md5Hash))
					} else {
						files = append(files, rel+":0:")
					}
				}
				return nil
			})
			fmt.Fprintf(conn, "FILES %s\n", strings.Join(files, ","))

		case "DOWNLOAD":
			if len(parts) < 2 {
				fmt.Fprintf(conn, "ERROR brak sciezki\n")
				continue
			}
			rawPath := filepath.Clean("/" + parts[1])
			pathParts := strings.SplitN(strings.TrimPrefix(rawPath, "/"), "/", 2)
			var cleanPath string
			if len(pathParts) == 2 {
				cleanPath = filepath.Join(gamesDir, sanitizeGameName(pathParts[0]), pathParts[1])
			} else {
				cleanPath = filepath.Join(gamesDir, sanitizeGameName(pathParts[0]))
			}
			f, err := os.Open(cleanPath)
			if err != nil {
				fmt.Fprintf(conn, "ERROR nie znaleziono: %s\n", parts[1])
				continue
			}
			stat, _ := f.Stat()
			fmt.Fprintf(conn, "SIZE %d\n", stat.Size())
			buf := make([]byte, 1<<20)
			n, _ := io.CopyBuffer(conn, f, buf)
			f.Close()
			log.Printf("[TCP] %s → %s (%.1f MB)", parts[1], conn.RemoteAddr(), float64(n)/1024/1024)

		case "SPEEDTEST":
			size := int64(1 << 30)
			if len(parts) >= 2 {
				size, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			}
			fmt.Fprintf(conn, "READY %d\n", size)
			buf := make([]byte, 1<<20)
			var sent int64
			for sent < size {
				chunk := int64(len(buf))
				if sent+chunk > size {
					chunk = size - sent
				}
				n, err := conn.Write(buf[:chunk])
				sent += int64(n)
				if err != nil {
					break
				}
			}
			log.Printf("[TCP] SPEEDTEST: %.1f MB → %s", float64(sent)/1024/1024, conn.RemoteAddr())

		default:
			fmt.Fprintf(conn, "ERROR nieznana komenda\n")
		}
	}
}

func startTCPServer() {
	ln, err := net.Listen("tcp", ":8082")
	if err != nil {
		log.Printf("Blad TCP: %v", err)
		return
	}
	log.Println("TCP serwer gier uruchomiony na :8082")
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleTCPClient(conn)
	}
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	json.NewEncoder(w).Encode(map[string]string{"status": "OK", "version": "1.5.0"})
}

func adminTokenHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if checkAdmin(r) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	} else {
		http.Error(w, "Unauthorized", 401)
	}
}

// --- Comments ---
type Comment struct {
	ID        int    `json:"id"`
	GameName  string `json:"game_name"`
	Username  string `json:"username"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func getCommentsHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	gameName := r.URL.Query().Get("game")
	rows, _ := db.Query("SELECT id, username, content, created_at FROM comments WHERE game_name=$1 ORDER BY created_at DESC", gameName)
	defer rows.Close()
	var comments []Comment
	for rows.Next() {
		var c Comment
		var t time.Time
		rows.Scan(&c.ID, &c.Username, &c.Content, &t)
		c.GameName = gameName
		c.CreatedAt = t.Format("2006-01-02 15:04")
		comments = append(comments, c)
	}
	if comments == nil {
		comments = []Comment{}
	}
	json.NewEncoder(w).Encode(comments)
}

func addCommentHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	var req Comment
	json.NewDecoder(r.Body).Decode(&req)
	if req.GameName == "" || req.Username == "" || req.Content == "" {
		http.Error(w, "Bad request", 400)
		return
	}
	db.Exec("INSERT INTO comments (game_name, username, content) VALUES ($1,$2,$3)", req.GameName, req.Username, req.Content)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Reactions ---
type ReactionReq struct {
	GameName string `json:"game_name"`
	Username string `json:"username"`
	IsUpvote bool   `json:"is_upvote"`
}

func reactionHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	var req ReactionReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.GameName == "" || req.Username == "" {
		http.Error(w, "Bad request", 400)
		return
	}

	var existing bool
	err := db.QueryRow("SELECT is_upvote FROM game_reactions WHERE game_name=$1 AND username=$2", req.GameName, req.Username).Scan(&existing)

	tx, _ := db.Begin()
	if err == sql.ErrNoRows {
		tx.Exec("INSERT INTO game_reactions (game_name, username, is_upvote) VALUES ($1,$2,$3)", req.GameName, req.Username, req.IsUpvote)
		if req.IsUpvote {
			tx.Exec("UPDATE games SET upvotes = upvotes + 1 WHERE name=$1", req.GameName)
		} else {
			tx.Exec("UPDATE games SET downvotes = downvotes + 1 WHERE name=$1", req.GameName)
		}
	} else if err == nil {
		if existing != req.IsUpvote {
			tx.Exec("UPDATE game_reactions SET is_upvote=$3 WHERE game_name=$1 AND username=$2", req.GameName, req.Username, req.IsUpvote)
			if req.IsUpvote {
				tx.Exec("UPDATE games SET upvotes = upvotes + 1, downvotes = downvotes - 1 WHERE name=$1", req.GameName)
			} else {
				tx.Exec("UPDATE games SET upvotes = upvotes - 1, downvotes = downvotes + 1 WHERE name=$1", req.GameName)
			}
		} else {
			tx.Exec("DELETE FROM game_reactions WHERE game_name=$1 AND username=$2", req.GameName, req.Username)
			if req.IsUpvote {
				tx.Exec("UPDATE games SET upvotes = upvotes - 1 WHERE name=$1", req.GameName)
			} else {
				tx.Exec("UPDATE games SET downvotes = downvotes - 1 WHERE name=$1", req.GameName)
			}
		}
	}
	tx.Commit()
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Reposts ---
type RepostReq struct {
	GameName string `json:"game_name"`
	Username string `json:"username"`
}

func addRepostHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
	var req RepostReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.GameName == "" || req.Username == "" {
		http.Error(w, "Bad request", 400)
		return
	}
	db.Exec("INSERT INTO user_reposts (username, game_name) VALUES ($1,$2) ON CONFLICT DO NOTHING", req.Username, req.GameName)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func getProfileHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	username := r.URL.Query().Get("user")
	rows, _ := db.Query("SELECT game_name FROM user_reposts WHERE username=$1 ORDER BY reposted_at DESC", username)
	defer rows.Close()
	var reposts []string
	for rows.Next() {
		var g string
		rows.Scan(&g)
		reposts = append(reposts, g)
	}
	if reposts == nil {
		reposts = []string{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"username": username,
		"reposts":  reposts,
	})
}

func main() {
	initDB()
	go startTCPServer()

	// Publiczne
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/register", registerHandler)
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/messages", messagesHandler)
	http.HandleFunc("/pm/history", pmHistoryHandler)
	http.HandleFunc("/pm/conversations", pmConversationsHandler)
	http.HandleFunc("/ws", chatHandler)
	http.HandleFunc("/games", gamesListHandler)

	http.HandleFunc("/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "OPTIONS" {
			getCommentsHandler(w, r)
		} else if r.Method == "POST" {
			addCommentHandler(w, r)
		}
	})
	http.HandleFunc("/reactions", reactionHandler)
	http.HandleFunc("/reposts", addRepostHandler)
	http.HandleFunc("/profile", getProfileHandler)

	// Admin API
	http.HandleFunc("/admin/auth", adminTokenHandler)
	http.HandleFunc("/admin/games", adminGamesListHandler)
	http.HandleFunc("/admin/games/upload", adminUploadHandler)
	http.HandleFunc("/admin/games/delete", adminDeleteHandler)
	http.HandleFunc("/admin/games/meta", adminMetaHandler)
	http.HandleFunc("/admin/games/file", adminFileDeleteHandler) // usuwanie pliku
	http.HandleFunc("/admin/games/move", adminFileMoveHandler)   // przenoszenie pliku

	// Statyczne pliki gier przez HTTP (opcjonalnie)
	http.Handle("/games/files/", http.StripPrefix("/games/files/", http.FileServer(http.Dir(gamesDir))))

	fmt.Println("HTTP :8080  |  TCP :8082  |  Admin token:", adminToken)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// Pomocnicza funkcja – oblicz rozmiar katalogu
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, _ := d.Info()
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// Użyta w adminUploadHandler – supresja "unused"
var _ = bytes.NewReader
var _ = strconv.Itoa

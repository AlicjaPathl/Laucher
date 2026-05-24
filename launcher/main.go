package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/gorilla/websocket"
)

var serverURL = "http://127.0.0.1:8080"
var wsURL = "ws://127.0.0.1:8080/ws"
var tcpAddr = "127.0.0.1:8082"

var wsConn *websocket.Conn
var currentUser string

// --- Steam dark theme ---
type steamTheme struct{}

func (steamTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 27, G: 40, B: 56, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 42, G: 71, B: 94, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 71, G: 191, B: 255, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 199, G: 213, B: 224, A: 255}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 42, G: 71, B: 94, A: 255}
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 42, G: 71, B: 94, A: 255}
	case theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 143, G: 152, B: 160, A: 255}
	}
	return theme.DefaultTheme().Color(n, v)
}
func (steamTheme) Font(s fyne.TextStyle) fyne.Resource   { return theme.DefaultTheme().Font(s) }
func (steamTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }
func (steamTheme) Size(n fyne.ThemeSizeName) float32     { return theme.DefaultTheme().Size(n) }

// --- API ---
type authRequest struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func apiPost(endpoint string, payload interface{}) (map[string]string, error) {
	data, _ := json.Marshal(payload)
	resp, err := http.Post(serverURL+endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("brak połączenia z serwerem (port 8080)")
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

// --- Global Chat ---
type ChatMsg struct {
	Username string `json:"username"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

func loadHistory(messages *[]string, msgList *widget.List) {
	resp, err := http.Get(serverURL + "/messages")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var history []ChatMsg
	json.NewDecoder(resp.Body).Decode(&history)
	for _, m := range history {
		*messages = append(*messages, fmt.Sprintf("[%s] %s: %s", m.SentAt, m.Username, m.Content))
	}
	fyne.Do(func() {
		msgList.Refresh()
	})
}

func connectChat(msgList *widget.List, messages *[]string, pmDispatch func(from, content string)) {
	wsURLWithUser := wsURL + "?user=" + currentUser
	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURLWithUser, nil)
	if err != nil {
		return
	}
	wsConn = conn
	*messages = append(*messages, "[System] Połączono z czatem jako "+currentUser)
	fyne.Do(func() {
		msgList.Refresh()
	})

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			text := string(msg)
			if strings.HasPrefix(text, "GLOBAL:") {
				parts := strings.SplitN(text, ":", 3)
				if len(parts) == 3 {
					*messages = append(*messages, parts[1]+": "+parts[2])
					fyne.Do(func() {
						msgList.Refresh()
					})
				}
			} else if strings.HasPrefix(text, "PM:") {
				// PM:from:to:content
				parts := strings.SplitN(text, ":", 4)
				if len(parts) == 4 && pmDispatch != nil {
					pmDispatch(parts[1], parts[3])
				}
			}
		}
	}()
}

func sendGlobal(text string) {
	if wsConn == nil || text == "" {
		return
	}
	msg := fmt.Sprintf("GLOBAL:%s:%s", currentUser, text)
	wsConn.WriteMessage(websocket.TextMessage, []byte(msg))
}

func sendPM(to, text string) {
	if wsConn == nil || text == "" {
		return
	}
	msg := fmt.Sprintf("PM:%s:%s:%s", currentUser, to, text)
	wsConn.WriteMessage(websocket.TextMessage, []byte(msg))
}

// --- Private messages history ---
type PMMsg struct {
	FromUser string `json:"from_user"`
	ToUser   string `json:"to_user"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

func loadPMHistory(partner string) []PMMsg {
	resp, err := http.Get(fmt.Sprintf("%s/pm/history?from=%s&to=%s", serverURL, currentUser, partner))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var msgs []PMMsg
	json.NewDecoder(resp.Body).Decode(&msgs)
	return msgs
}

// --- Playtime and Local Installation Manager ---
const installedGamesDir = "./installed_games"
const playtimeFile = "playtime.json"
const ownedGamesFile = "owned_games.json"

func isGameOwned(gameID string) bool {
	data, err := os.ReadFile(ownedGamesFile)
	if err != nil {
		return false
	}
	var owned map[string]bool
	json.Unmarshal(data, &owned)
	return owned[gameID]
}

func addGameToLibrary(gameID string) {
	var owned map[string]bool
	data, err := os.ReadFile(ownedGamesFile)
	if err == nil {
		json.Unmarshal(data, &owned)
	}
	if owned == nil {
		owned = make(map[string]bool)
	}
	owned[gameID] = true
	updated, _ := json.Marshal(owned)
	os.WriteFile(ownedGamesFile, updated, 0644)
}

func getLocalPlaytime(gameID string) string {
	data, err := os.ReadFile(playtimeFile)
	if err != nil {
		return "Nie grano jeszcze"
	}
	var playtime map[string]float64
	json.Unmarshal(data, &playtime)
	seconds, ok := playtime[gameID]
	if !ok || seconds <= 0 {
		return "Nie grano jeszcze"
	}
	duration := time.Duration(seconds) * time.Second
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func addLocalPlaytime(gameID string, duration time.Duration) {
	var playtime map[string]float64
	data, err := os.ReadFile(playtimeFile)
	if err == nil {
		json.Unmarshal(data, &playtime)
	}
	if playtime == nil {
		playtime = make(map[string]float64)
	}
	playtime[gameID] += duration.Seconds()
	updatedData, _ := json.Marshal(playtime)
	os.WriteFile(playtimeFile, updatedData, 0644)
}

func getLocalGameVersion(gameID string) string {
	versionPath := filepath.Join(installedGamesDir, gameID, "version.txt")
	data, err := os.ReadFile(versionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func isGameInstalled(gameID string) bool {
	versionPath := filepath.Join(installedGamesDir, gameID, "version.txt")
	_, err := os.Stat(versionPath)
	return err == nil
}

func uninstallGame(gameID string) {
	os.RemoveAll(filepath.Join(installedGamesDir, gameID))
}

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

// tcpFetchFileList otwiera oddzielne połączenie i pobiera listę plików
func tcpFetchFileList(gameID string) ([]string, error) {
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(conn, "FILES %s\n", gameID)
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("błąd czytania listy: %v", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "FILES ") {
		return nil, fmt.Errorf("nieoczekiwana odpowiedź: %s", line)
	}
	filesStr := strings.TrimPrefix(line, "FILES ")
	if filesStr == "" {
		return []string{}, nil
	}
	return strings.Split(filesStr, ","), nil
}

// tcpDownloadSingleFile pobiera jeden plik na własnym połączeniu TCP – eliminuje bug bufio
func tcpDownloadSingleFile(gameID, remotePath, localFullPath string) error {
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("błąd połączenia: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "DOWNLOAD %s/%s\n", gameID, remotePath)
	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("błąd odpowiedzi: %v", err)
	}
	respLine = strings.TrimRight(respLine, "\r\n")
	if strings.HasPrefix(respLine, "ERROR") {
		return fmt.Errorf("serwer: %s", respLine)
	}
	parts := strings.SplitN(respLine, " ", 2)
	if len(parts) != 2 || parts[0] != "SIZE" {
		return fmt.Errorf("nieoczekiwana odpowiedź: %s", respLine)
	}
	fileSize, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)

	os.MkdirAll(filepath.Dir(localFullPath), 0755)
	f, err := os.Create(localFullPath)
	if err != nil {
		return fmt.Errorf("błąd pliku lokalnego: %v", err)
	}
	_, copyErr := io.CopyN(f, reader, fileSize)
	f.Close()
	if copyErr != nil && copyErr != io.EOF {
		os.Remove(localFullPath)
		return fmt.Errorf("błąd pobierania: %v", copyErr)
	}
	os.Chmod(localFullPath, 0755)
	return nil
}

func tcpDownloadGame(gameID string, localDirName string, targetVersion string, targetOS string, progress *widget.ProgressBar, statusLbl *widget.Label, onDone func()) {
	fyne.Do(func() {
		statusLbl.SetText("Łączenie z serwerem...")
		progress.SetValue(0)
	})

	// 1. Pobierz listę plików (osobne połączenie)
	allFiles, err := tcpFetchFileList(gameID)
	if err != nil {
		fyne.Do(func() { statusLbl.SetText("Błąd listy: " + err.Error()) })
		return
	}
	if len(allFiles) == 0 {
		fyne.Do(func() { statusLbl.SetText("Brak plików gry na serwerze") })
		return
	}

	// 2. Filtruj po platformie
	type downloadTask struct {
		remotePath   string
		localPath    string
		expectedSize int64
		expectedMD5  string
	}
	var tasks []downloadTask
	for _, rawFile := range allFiles {
		rawFile = strings.TrimSpace(rawFile)
		if rawFile == "" {
			continue
		}
		parts := strings.Split(rawFile, ":")
		remoteFile := parts[0]
		var size int64
		var md5Val string
		if len(parts) >= 2 { size, _ = strconv.ParseInt(parts[1], 10, 64) }
		if len(parts) >= 3 { md5Val = parts[2] }

		isWin := strings.HasPrefix(remoteFile, "windows/")
		isLin := strings.HasPrefix(remoteFile, "linux/")
		if isWin {
			if targetOS == "windows" {
				tasks = append(tasks, downloadTask{remoteFile, strings.TrimPrefix(remoteFile, "windows/"), size, md5Val})
			}
		} else if isLin {
			if targetOS == "linux" || targetOS == "darwin" {
				tasks = append(tasks, downloadTask{remoteFile, strings.TrimPrefix(remoteFile, "linux/"), size, md5Val})
			}
		} else {
			tasks = append(tasks, downloadTask{remoteFile, remoteFile, size, md5Val})
		}
	}

	if len(tasks) == 0 {
		fyne.Do(func() { statusLbl.SetText("Brak plików dla: " + targetOS) })
		return
	}

	gameRootDir := filepath.Join(installedGamesDir, localDirName)
	allowedPaths := make(map[string]bool)

	fyne.Do(func() { statusLbl.SetText(fmt.Sprintf("Sprawdzanie %d plików...", len(tasks))) })

	// 3. Każdy plik = osobne połączenie TCP (fix dla zamrożenia pobierania)
	for i, task := range tasks {
		localFullPath := filepath.Join(gameRootDir, task.localPath)
		allowedPaths[filepath.Clean(localFullPath)] = true

		// Sprawdź czy plik jest już aktualny
		skip := false
		if stat, err2 := os.Stat(localFullPath); err2 == nil && !stat.IsDir() && stat.Size() == task.expectedSize {
			if task.expectedMD5 == "" {
				skip = true
			} else if getLocalFileMD5(localFullPath) == task.expectedMD5 {
				skip = true
			}
		}

		iVal, taskVal := i, task
		if skip {
			fyne.Do(func() {
				statusLbl.SetText(fmt.Sprintf("[%d/%d] OK: %s", iVal+1, len(tasks), taskVal.localPath))
				progress.SetValue(float64(iVal+1) / float64(len(tasks)))
			})
			continue
		}

		fyne.Do(func() {
			statusLbl.SetText(fmt.Sprintf("[%d/%d] Pobieranie: %s", iVal+1, len(tasks), taskVal.localPath))
		})

		if dlErr := tcpDownloadSingleFile(gameID, task.remotePath, localFullPath); dlErr != nil {
			fyne.Do(func() { statusLbl.SetText(fmt.Sprintf("Błąd [%s]: %v", taskVal.localPath, dlErr)) })
			return
		}
		fyne.Do(func() { progress.SetValue(float64(iVal+1) / float64(len(tasks))) })
	}

	// 4. Usuń stare pliki
	versionFileClean := filepath.Clean(filepath.Join(gameRootDir, "version.txt"))
	filepath.WalkDir(gameRootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		cp := filepath.Clean(path)
		if cp != versionFileClean && !allowedPaths[cp] {
			os.Remove(cp)
		}
		return nil
	})

	// 5. Zapisz wersję
	os.WriteFile(filepath.Join(gameRootDir, "version.txt"), []byte(targetVersion), 0644)

	fyne.Do(func() {
		statusLbl.SetText("✓ Gotowe!")
		progress.SetValue(1)
	})
	if onDone != nil {
		fyne.Do(onDone)
	}

}

func runLocalGame(gameID string, localDirName string, mainExe string, useWine bool, statusLbl *widget.Label, onDone func(duration time.Duration, err error)) {
	gameDir := filepath.Join(installedGamesDir, localDirName)
	var exePath string

	// Clean up platform prefix if there is one (e.g. "windows/game.exe" -> "game.exe")
	cleanMainExe := mainExe
	if strings.HasPrefix(strings.ToLower(cleanMainExe), "windows/") {
		cleanMainExe = strings.TrimPrefix(cleanMainExe, "windows/")
	} else if strings.HasPrefix(strings.ToLower(cleanMainExe), "linux/") {
		cleanMainExe = strings.TrimPrefix(cleanMainExe, "linux/")
	}

	if cleanMainExe != "" {
		c := filepath.Join(gameDir, cleanMainExe)
		if stat, err := os.Stat(c); err == nil && !stat.IsDir() {
			exePath = c
		}
	}

	// Fallback do automatycznego wykrywania jeśli mainExe nie jest zdefiniowane lub nie istnieje
	if exePath == "" {
		candidates := []string{
			filepath.Join(gameDir, gameID),
			filepath.Join(gameDir, gameID+".exe"),
			filepath.Join(gameDir, gameID+".x86_64"),
			filepath.Join(gameDir, gameID+".bin"),
			filepath.Join(gameDir, gameID+".sh"),
		}
		for _, c := range candidates {
			if stat, err := os.Stat(c); err == nil && !stat.IsDir() {
				exePath = c
				break
			}
		}
	}

	if exePath == "" {
		filepath.WalkDir(gameDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := strings.ToLower(d.Name())
			if name == "version.txt" || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".pak") {
				return nil
			}
			if exePath == "" {
				exePath = path
			}
			return nil
		})
	}

	if exePath == "" {
		onDone(0, fmt.Errorf("nie znaleziono pliku wykonywalnego gry"))
		return
	}

	// Make paths absolute to avoid Go exec relative path gotcha when cmd.Dir is set
	if abs, err := filepath.Abs(exePath); err == nil {
		exePath = abs
	}
	if abs, err := filepath.Abs(gameDir); err == nil {
		gameDir = abs
	}

	// Proactively make the file executable on unix-like systems
	os.Chmod(exePath, 0755)

	fyne.Do(func() {
		statusLbl.SetText("Gra jest uruchomiona...")
	})
	startTime := time.Now()

	go func() {
		var cmd *exec.Cmd
		if useWine {
			cmd = exec.Command("wine", exePath)
		} else {
			cmd = exec.Command(exePath)
		}
		cmd.Dir = gameDir
		err := cmd.Run()
		elapsed := time.Since(startTime)
		fyne.Do(func() {
			onDone(elapsed, err)
		})
	}()
}

// ========== MAIN ==========
func main() {
	a := app.New()
	a.Settings().SetTheme(&steamTheme{})
	w := a.NewWindow("Neon Game Launcher")
	w.Resize(fyne.NewSize(1100, 720))

	// ===== AUTH SCREEN =====
	isRegister := false
	title := widget.NewLabelWithStyle("LOGOWANIE", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	errorLabel := widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{})
	errorLabel.Wrapping = fyne.TextWrapWord
	emailEntry := widget.NewEntry()
	emailEntry.SetPlaceHolder("Adres E-mail")
	emailEntry.Hide()
	usernameEntry := widget.NewEntry()
	usernameEntry.SetPlaceHolder("Nazwa użytkownika")
	passwordEntry := widget.NewPasswordEntry()
	passwordEntry.SetPlaceHolder("Hasło")

	var toggleLink *widget.Hyperlink
	var authBtn *widget.Button
	showMain := func() {}

	authBtn = widget.NewButton("Zaloguj się", func() {
		errorLabel.SetText("")
		username := usernameEntry.Text
		password := passwordEntry.Text
		if username == "" || password == "" {
			errorLabel.SetText("Wypełnij wszystkie pola!")
			return
		}
		if isRegister {
			email := emailEntry.Text
			if email == "" {
				errorLabel.SetText("Wpisz adres e-mail!")
				return
			}
			_, err := apiPost("/register", authRequest{Email: email, Username: username, Password: password})
			if err != nil {
				errorLabel.SetText(err.Error())
				return
			}
			errorLabel.SetText("Zarejestrowano! Możesz się zalogować.")
			isRegister = false
			title.SetText("LOGOWANIE")
			authBtn.SetText("Zaloguj się")
			toggleLink.SetText("Nie masz konta? Zarejestruj się")
			emailEntry.Hide()
		} else {
			_, err := apiPost("/login", authRequest{Username: username, Password: password})
			if err != nil {
				errorLabel.SetText(err.Error())
				return
			}
			currentUser = username
			showMain()
		}
	})

	toggleLink = widget.NewHyperlink("Nie masz konta? Zarejestruj się", &url.URL{})
	toggleLink.OnTapped = func() {
		isRegister = !isRegister
		errorLabel.SetText("")
		if isRegister {
			title.SetText("REJESTRACJA")
			authBtn.SetText("Zarejestruj się")
			toggleLink.SetText("Masz już konto? Zaloguj się")
			emailEntry.Show()
		} else {
			title.SetText("LOGOWANIE")
			authBtn.SetText("Zaloguj się")
			toggleLink.SetText("Nie masz konta? Zarejestruj się")
			emailEntry.Hide()
		}
	}

	authForm := container.NewVBox(title, errorLabel, emailEntry, usernameEntry, passwordEntry, authBtn, container.NewCenter(toggleLink))
	authScreen := container.NewCenter(container.NewGridWrap(fyne.NewSize(340, 330), authForm))

	// ===== MAIN SCREEN =====
	showMain = func() {
		// ---- GLOBAL DATA MODEL ----
		type GameMetadata struct {
			Name           string `json:"name"`
			DisplayName    string `json:"display_name"`
			Description    string `json:"description"`
			Version        string `json:"version"`
			SizeBytes      int64  `json:"size_bytes"`
			MainExeWindows string `json:"main_exe_windows"`
			MainExeLinux   string `json:"main_exe_linux"`
			Category       string `json:"category"`
			Upvotes        int    `json:"upvotes"`
			Downvotes      int    `json:"downvotes"`
		}

		var allServerGames []GameMetadata
		var ownedGames []GameMetadata

		// Lists definitions
		var shopListWidget *widget.List
		var libListWidget *widget.List

		var selectedShopGame *GameMetadata
		var selectedLibGame *GameMetadata

		// Dynamic callback function definitions
		var refreshShopRightPanel func()
		var refreshLibRightPanel func()
		var refreshAllLists func()

		// ---- SKLEP (Shop Tab) ----
		shopLabel := widget.NewLabelWithStyle("Sklep gier", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

		shopDetailName := widget.NewLabelWithStyle("Wybierz grę z katalogu", fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Italic: true})
		shopDetailDesc := widget.NewLabel("")
		shopDetailDesc.Wrapping = fyne.TextWrapWord
		shopDetailMeta := widget.NewLabel("")
		shopDetailMeta.Hide()

		addToLibBtn := widget.NewButton("Dodaj do biblioteki", nil)
		addToLibBtn.Hide()

		upvoteBtn := widget.NewButton("👍", nil)
		upvoteBtn.Hide()
		downvoteBtn := widget.NewButton("👎", nil)
		downvoteBtn.Hide()
		repostBtn := widget.NewButton("🔄 Repostuj", nil)
		repostBtn.Hide()

		commentInput := widget.NewEntry()
		commentInput.SetPlaceHolder("Napisz komentarz...")
		commentBtn := widget.NewButton("Wyślij", nil)
		
		var commentsList *widget.List
		var commentsData []string
		
		refreshComments := func() {
			if selectedShopGame == nil { return }
			resp, err := http.Get(serverURL + "/comments?game=" + selectedShopGame.Name)
			if err != nil { return }
			defer resp.Body.Close()
			var cmts []map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&cmts)
			commentsData = nil
			for _, c := range cmts {
				commentsData = append(commentsData, fmt.Sprintf("[%s] %s: %s", c["created_at"], c["username"], c["content"]))
			}
			if commentsList != nil { commentsList.Refresh() }
		}

		refreshShopRightPanel = func() {
			if selectedShopGame == nil {
				shopDetailName.SetText("Wybierz grę z katalogu")
				shopDetailDesc.SetText("")
				shopDetailMeta.Hide()
				addToLibBtn.Hide()
				return
			}

			shopDetailName.SetText("🛍 " + selectedShopGame.DisplayName)
			shopDetailDesc.SetText(selectedShopGame.Description)
			shopDetailMeta.SetText(fmt.Sprintf("Wersja: v%s | Rozmiar: %.1f MB\nKategoria: %s | 👍 %d  👎 %d", 
				selectedShopGame.Version, float64(selectedShopGame.SizeBytes)/1024/1024,
				selectedShopGame.Category, selectedShopGame.Upvotes, selectedShopGame.Downvotes))
			shopDetailMeta.Show()

			if isGameOwned(selectedShopGame.Name) {
				addToLibBtn.SetText("W bibliotece")
				addToLibBtn.Importance = widget.LowImportance
				addToLibBtn.Disable()
			} else {
				addToLibBtn.SetText("Dodaj do biblioteki")
				addToLibBtn.Importance = widget.SuccessImportance
				addToLibBtn.Enable()
			}
			addToLibBtn.Show()
			upvoteBtn.Show()
			downvoteBtn.Show()
			repostBtn.Show()
			refreshComments()
		}

		repostBtn.OnTapped = func() {
			if selectedShopGame == nil { return }
			apiPost("/reposts", map[string]interface{}{"game_name": selectedShopGame.Name, "username": currentUser})
			dialog.ShowInformation("Repost", "Dodano do Twojego profilu!", w)
		}

		commentBtn.OnTapped = func() {
			if selectedShopGame == nil || commentInput.Text == "" { return }
			apiPost("/comments", map[string]interface{}{"game_name": selectedShopGame.Name, "username": currentUser, "content": commentInput.Text})
			commentInput.SetText("")
			refreshComments()
		}

		upvoteBtn.OnTapped = func() {
			if selectedShopGame == nil { return }
			apiPost("/reactions", map[string]interface{}{"game_name": selectedShopGame.Name, "username": currentUser, "is_upvote": true})
			refreshAllLists()
		}
		downvoteBtn.OnTapped = func() {
			if selectedShopGame == nil { return }
			apiPost("/reactions", map[string]interface{}{"game_name": selectedShopGame.Name, "username": currentUser, "is_upvote": false})
			refreshAllLists()
		}

		addToLibBtn.OnTapped = func() {
			if selectedShopGame == nil {
				return
			}
			addGameToLibrary(selectedShopGame.Name)
			dialog.ShowInformation("Sklep", fmt.Sprintf("Dodano '%s' do Twojej biblioteki!", selectedShopGame.DisplayName), w)
			refreshAllLists()
		}

		shopListWidget = widget.NewList(
			func() int { return len(allServerGames) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText("🛍 " + allServerGames[i].DisplayName)
			},
		)

		shopListWidget.OnSelected = func(id widget.ListItemID) {
			if id < len(allServerGames) {
				selectedShopGame = &allServerGames[id]
				refreshShopRightPanel()
			}
		}

		leftShopPanel := container.NewBorder(
			container.NewVBox(
				widget.NewLabelWithStyle("Katalog gier", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewButton("Odśwież sklep", func() { refreshAllLists() }),
				widget.NewSeparator(),
			),
			nil, nil, nil,
			shopListWidget,
		)

		commentsList = widget.NewList(
			func() int { return len(commentsData) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText(commentsData[i])
			},
		)

		rightShopPanel := container.NewBorder(
			container.NewVBox(
				shopDetailName,
				widget.NewSeparator(),
				shopDetailMeta,
				widget.NewSeparator(),
			),
			container.NewVBox(
				widget.NewSeparator(),
				container.NewGridWithColumns(4, addToLibBtn, upvoteBtn, downvoteBtn, repostBtn),
				container.NewBorder(nil, nil, nil, commentBtn, commentInput),
			),
			nil, nil,
			container.NewVSplit(container.NewScroll(shopDetailDesc), commentsList),
		)

		shopTabContent := container.NewHSplit(leftShopPanel, rightShopPanel)
		shopTabContent.SetOffset(0.3)
		shopTab := container.NewBorder(shopLabel, nil, nil, nil, shopTabContent)

		// ---- BIBLIOTEKA (Library Tab) ----
		libLabel := widget.NewLabelWithStyle("Twoja biblioteka", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

		libDetailName := widget.NewLabelWithStyle("Wybierz grę z biblioteki", fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Italic: true})
		libDetailDesc := widget.NewLabel("")
		libDetailDesc.Wrapping = fyne.TextWrapWord
		libDetailPlaytime := widget.NewLabel("")
		libDetailPlaytime.Hide()
		libDetailVersion := widget.NewLabel("")
		libDetailVersion.Hide()

		dlProgress := widget.NewProgressBar()
		dlProgress.Hide()
		dlStatus := widget.NewLabel("")
		dlStatus.Hide()

		var actionBtn *widget.Button
		var uninstallBtn *widget.Button
		var crossDlBtn *widget.Button

		wineCheck := widget.NewCheck("Uruchom przez Wine (pobierz wersję Windows)", func(checked bool) {
			refreshLibRightPanel()
		})
		wineCheck.Hide()

		refreshLibRightPanel = func() {
			if selectedLibGame == nil {
				libDetailName.SetText("Wybierz grę z biblioteki")
				libDetailDesc.SetText("")
				libDetailPlaytime.Hide()
				libDetailVersion.Hide()
				actionBtn.Hide()
				uninstallBtn.Hide()
				wineCheck.Hide()
				if crossDlBtn != nil { crossDlBtn.Hide() }
				dlProgress.Hide()
				dlStatus.Hide()
				return
			}

			libDetailName.SetText("🎮 " + selectedLibGame.DisplayName)
			libDetailDesc.SetText(selectedLibGame.Description)

			// Playtime
			playtimeStr := getLocalPlaytime(selectedLibGame.Name)
			libDetailPlaytime.SetText("Rozegrany czas: " + playtimeStr)
			libDetailPlaytime.Show()

			// Wine support visibility: only on Linux or Darwin
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				wineCheck.Show()
			} else {
				wineCheck.Hide()
			}

			// Local folder name changes depending on Wine checkbox
			localDirName := selectedLibGame.Name
			if wineCheck.Checked {
				localDirName = selectedLibGame.Name + "_wine"
				if !isGameInstalled(localDirName) && isGameInstalled(selectedLibGame.Name+"_windows") {
					localDirName = selectedLibGame.Name + "_windows"
				}
			}

			// Installed vs Server version check
			installed := isGameInstalled(localDirName)
			localVer := getLocalGameVersion(localDirName)

			if installed {
				libDetailVersion.SetText(fmt.Sprintf("Zainstalowana wersja: v%s | Dostępna: v%s", localVer, selectedLibGame.Version))
				libDetailVersion.Show()
				uninstallBtn.Show()

				if localVer != selectedLibGame.Version {
					actionBtn.SetText(fmt.Sprintf("Aktualizuj (do v%s)", selectedLibGame.Version))
					actionBtn.Importance = widget.WarningImportance
				} else {
					if wineCheck.Checked {
						actionBtn.SetText("  ▶  GRAJ (WINE)  ")
					} else {
						actionBtn.SetText("  ▶  GRAJ  ")
					}
					actionBtn.Importance = widget.SuccessImportance
				}
			} else {
				libDetailVersion.SetText(fmt.Sprintf("Dostępna wersja: v%s (Pobieranie: %.1f MB)", selectedLibGame.Version, float64(selectedLibGame.SizeBytes)/1024/1024))
				libDetailVersion.Show()
				if wineCheck.Checked {
					actionBtn.SetText("Pobierz wersję Windows (TCP)")
				} else {
					actionBtn.SetText("Pobierz grę (TCP)")
				}
				actionBtn.Importance = widget.MediumImportance
				uninstallBtn.Hide()
			}
			actionBtn.Show()

			// Cross-platform download button label
			if crossDlBtn != nil {
				if runtime.GOOS == "windows" {
					crossDlBtn.SetText("⬇ Pobierz wersję Linux")
				} else {
					crossDlBtn.SetText("⬇ Pobierz wersję Windows")
				}
				crossDlBtn.Show()
			}
		}

		actionBtn = widget.NewButton("Graj / Pobierz", func() {
			if selectedLibGame == nil {
				return
			}
			localDirName := selectedLibGame.Name
			if wineCheck.Checked {
				localDirName = selectedLibGame.Name + "_wine"
				if !isGameInstalled(localDirName) && isGameInstalled(selectedLibGame.Name+"_windows") {
					localDirName = selectedLibGame.Name + "_windows"
				}
			}
			installed := isGameInstalled(localDirName)
			localVer := getLocalGameVersion(localDirName)

			if installed && localVer == selectedLibGame.Version {
				// ▶ PLAY
				actionBtn.Disable()
				uninstallBtn.Disable()
				wineCheck.Disable()
				dlStatus.SetText("Gra jest uruchomiona...")
				dlStatus.Show()

				mainExe := selectedLibGame.MainExeLinux
				if runtime.GOOS == "windows" || wineCheck.Checked {
					mainExe = selectedLibGame.MainExeWindows
				}
				runLocalGame(selectedLibGame.Name, localDirName, mainExe, wineCheck.Checked, dlStatus, func(duration time.Duration, err error) {
					actionBtn.Enable()
					uninstallBtn.Enable()
					wineCheck.Enable()
					dlStatus.Hide()

					if err != nil {
						dialog.ShowError(fmt.Errorf("Błąd uruchomienia: %v", err), w)
						return
					}

					addLocalPlaytime(selectedLibGame.Name, duration)
					refreshLibRightPanel()
					dialog.ShowInformation("Launcher", fmt.Sprintf("Zakończono rozgrywkę. Czas sesji: %s", duration.Round(time.Second).String()), w)
				})
			} else {
				// DOWNLOAD / UPDATE
				actionBtn.Disable()
				uninstallBtn.Disable()
				wineCheck.Disable()
				dlProgress.SetValue(0)
				dlProgress.Show()
				dlStatus.Show()

				targetOS := runtime.GOOS
				if wineCheck.Checked {
					targetOS = "windows"
				}
				go tcpDownloadGame(selectedLibGame.Name, localDirName, selectedLibGame.Version, targetOS, dlProgress, dlStatus, func() {
					actionBtn.Enable()
					uninstallBtn.Enable()
					wineCheck.Enable()
					refreshLibRightPanel()
					dlProgress.Hide()
				})
			}
		})

		uninstallBtn = widget.NewButton("Odinstaluj grę", func() {
			if selectedLibGame == nil {
				return
			}
			localDirName := selectedLibGame.Name
			if wineCheck.Checked {
				localDirName = selectedLibGame.Name + "_wine"
				if !isGameInstalled(localDirName) && isGameInstalled(selectedLibGame.Name+"_windows") {
					localDirName = selectedLibGame.Name+"_windows"
				}
			}
			dialog.ShowConfirm("Odinstaluj grę", fmt.Sprintf("Czy na pewno usunąć pliki '%s'?", selectedLibGame.DisplayName), func(ok bool) {
				if ok {
					uninstallGame(localDirName)
					refreshLibRightPanel()
					dlStatus.SetText("Odinstalowano grę.")
					dlStatus.Show()
				}
			}, w)
		})

		// Cross-platform download button
		crossDlBtn = widget.NewButton("", nil)
		crossDlBtn.Hide()
		crossDlBtn.OnTapped = func() {
			if selectedLibGame == nil {
				return
			}
			var crossOS, crossDir, crossLabel string
			if runtime.GOOS == "windows" {
				crossOS = "linux"
				crossDir = selectedLibGame.Name + "_linux"
				crossLabel = "Pobieranie wersji Linux..."
			} else {
				crossOS = "windows"
				crossDir = selectedLibGame.Name + "_windows"
				crossLabel = "Pobieranie wersji Windows..."
			}
			actionBtn.Disable()
			uninstallBtn.Disable()
			wineCheck.Disable()
			crossDlBtn.Disable()
			dlProgress.SetValue(0)
			dlProgress.Show()
			dlStatus.SetText(crossLabel)
			dlStatus.Show()
			game := selectedLibGame
			go tcpDownloadGame(game.Name, crossDir, game.Version, crossOS, dlProgress, dlStatus, func() {
				actionBtn.Enable()
				uninstallBtn.Enable()
				wineCheck.Enable()
				crossDlBtn.Enable()
				refreshLibRightPanel()
				dlProgress.Hide()
			})
		}

		libListWidget = widget.NewList(
			func() int { return len(ownedGames) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText("🎮 " + ownedGames[i].DisplayName)
			},
		)

		libListWidget.OnSelected = func(id widget.ListItemID) {
			if id < len(ownedGames) {
				selectedLibGame = &ownedGames[id]
				refreshLibRightPanel()
			}
		}

		leftLibPanel := container.NewBorder(
			container.NewVBox(
				widget.NewLabelWithStyle("Twoje gry", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewButton("Odśwież bibliotekę", func() { refreshAllLists() }),
				widget.NewSeparator(),
			),
			nil, nil, nil,
			libListWidget,
		)

		rightLibPanel := container.NewBorder(
			container.NewVBox(
				libDetailName,
				widget.NewSeparator(),
				libDetailVersion,
				libDetailPlaytime,
				widget.NewSeparator(),
			),
			container.NewVBox(
				widget.NewSeparator(),
				wineCheck,
				container.NewGridWithColumns(2, actionBtn, uninstallBtn),
				crossDlBtn,
				dlProgress,
				dlStatus,
			),
			nil, nil,
			container.NewScroll(libDetailDesc),
		)

		// Set initial visibility
		actionBtn.Hide()
		uninstallBtn.Hide()

		libTabContent := container.NewHSplit(leftLibPanel, rightLibPanel)
		libTabContent.SetOffset(0.3)
		libTab := container.NewBorder(libLabel, nil, nil, nil, libTabContent)

		// ---- REFRESH ALL LISTS LOGIC ----
		refreshAllLists = func() {
			resp, err := http.Get(serverURL + "/games")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var list []GameMetadata
			json.NewDecoder(resp.Body).Decode(&list)
			allServerGames = list

			// Filter owned games
			var owned []GameMetadata
			for _, g := range list {
				if isGameOwned(g.Name) {
					owned = append(owned, g)
				}
			}
			ownedGames = owned

			// Safely wait for UI thread initialization
			time.Sleep(150 * time.Millisecond)

			// Refresh widgets
			shopListWidget.Refresh()
			libListWidget.Refresh()

			// Refresh details panels
			refreshShopRightPanel()
			refreshLibRightPanel()
		}

		// Will be run after SetContent

		// ---- COMMUNITY ----
		commTab := container.NewVBox(
			widget.NewLabelWithStyle("Społeczność", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Aktywność, recenzje, posty z warsztatu i statystyki graczy."),
		)

		// ---- CZAT GLOBALNY ----
		messages := []string{}
		msgList := widget.NewList(
			func() int { return len(messages) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				lbl := o.(*widget.Label)
				lbl.SetText(messages[i])
				lbl.Wrapping = fyne.TextWrapWord
			},
		)
		chatInput := widget.NewEntry()
		chatInput.SetPlaceHolder("Wpisz wiadomość...")
		sendBtn := widget.NewButton("Wyślij", func() {
			if chatInput.Text == "" {
				return
			}
			sendGlobal(chatInput.Text)
			chatInput.SetText("")
		})
		chatInput.OnSubmitted = func(s string) { sendBtn.OnTapped() }
		chatTab := container.NewBorder(nil,
			container.NewBorder(nil, nil, nil, sendBtn, chatInput),
			nil, nil, msgList,
		)

		// ---- PRYWATNY CZAT ----
		// Lewa kolumna: lista rozmów + pole nowej rozmowy
		pmMessages := []string{}
		pmPartner := ""

		pmMsgList := widget.NewList(
			func() int { return len(pmMessages) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				lbl := o.(*widget.Label)
				lbl.SetText(pmMessages[i])
				lbl.Wrapping = fyne.TextWrapWord
			},
		)

		pmPartnerLabel := widget.NewLabelWithStyle("Wybierz rozmowę", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

		openConversation := func(partner string) {
			pmPartner = partner
			pmPartnerLabel.SetText("Czat z: " + partner)
			pmMessages = []string{}
			history := loadPMHistory(partner)
			for _, m := range history {
				dir := "→"
				if m.FromUser == currentUser {
					dir = "Ty"
				} else {
					dir = m.FromUser
				}
				pmMessages = append(pmMessages, fmt.Sprintf("[%s] %s: %s", m.SentAt, dir, m.Content))
			}
			pmMsgList.Refresh()
		}

		// Dispatcher PM wiadomości przychodzących z WebSocket
		pmDispatch := func(from, content string) {
			if from == pmPartner || from == currentUser {
				dir := from
				if from == currentUser {
					dir = "Ty"
				}
				pmMessages = append(pmMessages, fmt.Sprintf("  %s: %s", dir, content))
				pmMsgList.Refresh()
			}
		}

		// Lista rozmów
		conversations := []string{}
		convList := widget.NewList(
			func() int { return len(conversations) },
			func() fyne.CanvasObject { return widget.NewButton("", nil) },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Button).SetText(conversations[i])
				o.(*widget.Button).OnTapped = func() { openConversation(conversations[i]) }
			},
		)
		// Odśwież listę rozmów
		refreshConvList := func() {
			resp, err := http.Get(fmt.Sprintf("%s/pm/conversations?user=%s", serverURL, currentUser))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var partners []string
			json.NewDecoder(resp.Body).Decode(&partners)
			conversations = partners
			convList.Refresh()
		}
		go refreshConvList()

		// Nowa rozmowa
		newConvEntry := widget.NewEntry()
		newConvEntry.SetPlaceHolder("Nazwa użytkownika...")
		newConvBtn := widget.NewButton("Otwórz czat", func() {
			if newConvEntry.Text == "" {
				return
			}
			partner := newConvEntry.Text
			newConvEntry.SetText("")
			// Dodaj do listy jeśli nie ma
			found := false
			for _, c := range conversations {
				if c == partner {
					found = true
				}
			}
			if !found {
				conversations = append(conversations, partner)
				convList.Refresh()
			}
			openConversation(partner)
		})

		// Pole wysyłania PM
		pmInput := widget.NewEntry()
		pmInput.SetPlaceHolder("Wiadomość prywatna...")
		pmSendBtn := widget.NewButton("Wyślij", func() {
			if pmPartner == "" || pmInput.Text == "" {
				return
			}
			sendPM(pmPartner, pmInput.Text)
			pmMessages = append(pmMessages, "Ty: "+pmInput.Text)
			pmMsgList.Refresh()
			pmInput.SetText("")
		})
		pmInput.OnSubmitted = func(s string) { pmSendBtn.OnTapped() }

		leftPanel := container.NewBorder(
			container.NewVBox(
				widget.NewLabelWithStyle("Rozmowy", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				container.NewBorder(nil, nil, nil, newConvBtn, newConvEntry),
				widget.NewSeparator(),
			),
			nil, nil, nil,
			convList,
		)

		rightPanel := container.NewBorder(
			pmPartnerLabel,
			container.NewBorder(nil, nil, nil, pmSendBtn, pmInput),
			nil, nil,
			pmMsgList,
		)

		pmTab := container.NewHSplit(leftPanel, rightPanel)
		pmTab.SetOffset(0.28)

		// Podłącz czat z dispatcherem PM
		connectChat(msgList, &messages, pmDispatch)
		go loadHistory(&messages, msgList)

		// --- Status bar ---
		statusLabel := widget.NewLabel("Serwer: sprawdzam...")
		go func() {
			resp, err := http.Get(serverURL + "/status")
			if err == nil && resp.StatusCode == 200 {
				fyne.Do(func() {
					statusLabel.SetText("HTTP :8080 ✓  TCP :8082 ✓")
				})
			} else {
				fyne.Do(func() {
					statusLabel.SetText("Serwer: Offline ✗")
				})
			}
		}()

		// --- Header ---
		headerLabel := canvas.NewText("NEON LAUNCHER", color.NRGBA{R: 71, G: 191, B: 255, A: 255})
		headerLabel.TextStyle = fyne.TextStyle{Bold: true}
		headerLabel.TextSize = 22
		userLabel := widget.NewLabel("Zalogowano jako: " + currentUser)
		header := container.NewBorder(nil, nil, container.NewPadded(headerLabel), container.NewHBox(userLabel))

		// --- Twój Profil ---
		var profileReposts []string
		profileList := widget.NewList(
			func() int { return len(profileReposts) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText("🔄 " + profileReposts[i])
			},
		)
		refreshProfile := func() {
			resp, err := http.Get(serverURL + "/profile?user=" + currentUser)
			if err == nil {
				defer resp.Body.Close()
				var p map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&p)
				if reps, ok := p["reposts"].([]interface{}); ok {
					profileReposts = nil
					for _, r := range reps {
						profileReposts = append(profileReposts, r.(string))
					}
					profileList.Refresh()
				}
			}
		}
		go refreshProfile()
		
		profileTab := container.NewBorder(
			container.NewVBox(
				widget.NewLabelWithStyle("Twój Profil: "+currentUser, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewLabel("Twoje zrepostowane aplikacje:"),
				widget.NewButton("Odśwież", func() { refreshProfile() }),
				widget.NewSeparator(),
			),
			nil, nil, nil,
			profileList,
		)

		tabs := container.NewAppTabs(
			container.NewTabItem("Profil", profileTab),
			container.NewTabItem("Sklep", shopTab),
			container.NewTabItem("Biblioteka", libTab),
			container.NewTabItem("Community", commTab),
			container.NewTabItem("Czat", chatTab),
			container.NewTabItem("PM", pmTab),
		)
		tabs.SetTabLocation(container.TabLocationTop)
		tabs.SelectIndex(1)

		mainScreen := container.NewBorder(
			container.NewVBox(header, widget.NewSeparator()),
			container.NewHBox(statusLabel),
			nil, nil,
			tabs,
		)
		w.SetContent(mainScreen)
	}

	w.SetContent(authScreen)
	w.ShowAndRun()
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
)

type adminTheme struct{}

func (adminTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 18, G: 18, B: 24, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 40, G: 60, B: 90, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 0, G: 200, B: 150, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 220, G: 230, B: 240, A: 255}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 30, G: 35, B: 50, A: 255}
	}
	return theme.DefaultTheme().Color(n, v)
}
func (adminTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (adminTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }
func (adminTheme) Size(n fyne.ThemeSizeName) float32       { return theme.DefaultTheme().Size(n) }

var (
	// Domyślne ustawienia produkcyjne – można nadpisać przez ldflags
	serverURL  = "http://pathl.pl:8080"
	tcpAddr    = "pathl.pl:8082"
	adminToken = "neon-admin-secret" // ZMIEŃ NA SWÓJ TAJNY TOKEN!
	mainWin    fyne.Window
)

type GameInfo struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	Version        string   `json:"version"`
	SizeBytes      int64    `json:"size_bytes"`
	MainExeWindows string   `json:"main_exe_windows"`
	MainExeLinux   string   `json:"main_exe_linux"`
	Files          []string `json:"files"`
	Category       string   `json:"category"`
}

func adminDo(method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, serverURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Admin-Token", adminToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	return http.DefaultClient.Do(req)
}

func fetchGames() ([]GameInfo, error) {
	resp, err := adminDo("GET", "/admin/games", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("Bledny token admina")
	}
	var games []GameInfo
	json.NewDecoder(resp.Body).Decode(&games)
	return games, nil
}

func deleteGame(name string) error {
	resp, err := adminDo("DELETE", "/admin/games/delete?game="+name, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func saveMeta(name, displayName, desc, version, mainExeWin, mainExeLin, category string) error {
	data, _ := json.Marshal(map[string]string{
		"name":             name,
		"display_name":     displayName,
		"description":      desc,
		"version":          version,
		"main_exe_windows": mainExeWin,
		"main_exe_linux":   mainExeLin,
		"category":         category,
	})
	req, _ := http.NewRequest("POST", serverURL+"/admin/games/meta", bytes.NewReader(data))
	req.Header.Set("X-Admin-Token", adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("serwer zwrocil status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// --- Nowe funkcje do zarządzania plikami ---

func deleteFile(game, filePath string) error {
	u := fmt.Sprintf("%s/admin/games/file?game=%s&file=%s",
		serverURL, url.QueryEscape(game), url.QueryEscape(filePath))
	req, _ := http.NewRequest("DELETE", u, nil)
	req.Header.Set("X-Admin-Token", adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func moveFile(game, oldPath, newPlatform string) error {
	u := fmt.Sprintf("%s/admin/games/move?game=%s&file=%s&platform=%s",
		serverURL, url.QueryEscape(game), url.QueryEscape(oldPath), url.QueryEscape(newPlatform))
	req, _ := http.NewRequest("POST", u, nil)
	req.Header.Set("X-Admin-Token", adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// platformIcon zwraca emoji i skrót platformy dla danej ścieżki
func platformIcon(filePath string) (icon, platform string) {
	parts := strings.SplitN(filepath.ToSlash(filePath), "/", 2)
	if len(parts) < 2 {
		return "📦", "common"
	}
	switch strings.ToLower(parts[0]) {
	case "linux":
		return "🐧", "linux"
	case "windows":
		return "🪟", "windows"
	default:
		return "📦", "common"
	}
}

type progressReader struct {
	r        io.Reader
	total    int64
	read     int64
	progress *widget.ProgressBar
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.read += int64(n)
	if pr.total > 0 {
		readVal := pr.read
		fyne.Do(func() {
			pr.progress.SetValue(float64(readVal) / float64(pr.total))
		})
	}
	return n, err
}

func uploadFile(gameName, localPath, destRelativePath string, progress *widget.ProgressBar, statusLbl *widget.Label) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, _ := f.Stat()
	total := stat.Size()
	pr := &progressReader{r: f, total: total, progress: progress}
	fyne.Do(func() {
		statusLbl.SetText(fmt.Sprintf("Wysylanie %s (%.1f MB)...", destRelativePath, float64(total)/1024/1024))
	})

	uploadURL := fmt.Sprintf("%s/admin/games/upload?game=%s&file=%s", serverURL, gameName, url.QueryEscape(destRelativePath))
	req, _ := http.NewRequest("POST", uploadURL, pr)
	req.Header.Set("X-Admin-Token", adminToken)
	req.ContentLength = total
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	fyne.Do(func() {
		progress.SetValue(1)
		statusLbl.SetText(fmt.Sprintf("Wyslano: %s", destRelativePath))
	})
	return nil
}

func uploadFolder(gameName, localFolderPath, platformPrefix string, progress *widget.ProgressBar, statusLbl *widget.Label) error {
	var files []string
	err := filepath.Walk(localFolderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	totalFiles := len(files)
	if totalFiles == 0 {
		return fmt.Errorf("wybrany folder jest pusty")
	}

	for i, filePath := range files {
		rel, err := filepath.Rel(localFolderPath, filePath)
		if err != nil {
			continue
		}
		destRel := rel
		if platformPrefix != "" {
			destRel = platformPrefix + "/" + rel
		}

		iVal := i
		fyne.Do(func() {
			statusLbl.SetText(fmt.Sprintf("[%d/%d] Wysylanie: %s", iVal+1, totalFiles, rel))
			progress.SetValue(float64(iVal) / float64(totalFiles))
		})

		err = uploadFile(gameName, filePath, destRel, progress, statusLbl)
		if err != nil {
			return fmt.Errorf("Blad przy pliku %s: %v", rel, err)
		}
	}
	fyne.Do(func() {
		progress.SetValue(1)
		statusLbl.SetText(fmt.Sprintf("Sukces! Wyslano caly folder (%d plikow).", totalFiles))
	})
	return nil
}

func runSpeedTest(sizeBytes int64, progress *widget.ProgressBar, statusLbl *widget.Label) {
	fyne.Do(func() {
		statusLbl.SetText("Laczenie z TCP :8082...")
	})
	conn, err := net.Dial("tcp", tcpAddr)
	if err != nil {
		fyne.Do(func() {
			statusLbl.SetText("Blad TCP: " + err.Error())
		})
		return
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "SPEEDTEST %d\n", sizeBytes)
	line, err := reader.ReadString('\n')
	if err != nil {
		fyne.Do(func() {
			statusLbl.SetText("Brak odpowiedzi serwera")
		})
		return
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "READY ") {
		fyne.Do(func() {
			statusLbl.SetText("Nieoczekiwana odpowiedz: " + line)
		})
		return
	}
	expected, _ := strconv.ParseInt(strings.TrimPrefix(line, "READY "), 10, 64)
	fyne.Do(func() {
		statusLbl.SetText(fmt.Sprintf("Pobieranie %.0f MB danych testowych...", float64(expected)/1024/1024))
	})
	buf := make([]byte, 1<<20)
	var received int64
	start := time.Now()
	for received < expected {
		n, err := reader.Read(buf)
		received += int64(n)
		if expected > 0 {
			receivedVal := received
			fyne.Do(func() {
				progress.SetValue(float64(receivedVal) / float64(expected))
			})
		}
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)
	mbps := float64(received) / 1024 / 1024 / elapsed.Seconds()
	gbps := mbps / 1024
	var speedStr string
	if gbps >= 0.1 {
		speedStr = fmt.Sprintf("%.2f Gb/s", gbps)
	} else {
		speedStr = fmt.Sprintf("%.1f MB/s", mbps)
	}
	result := "PASSED"
	if received < expected {
		result = fmt.Sprintf("FAILED – %d/%d bajtow", received, expected)
	}
	fyne.Do(func() {
		statusLbl.SetText(fmt.Sprintf("%s | Predkosc: %s | %.2fs | %.1f MB pobrano", result, speedStr, elapsed.Seconds(), float64(received)/1024/1024))
	})
}

func main() {
	a := app.NewWithID("pl.pathl.neon.admin")
	a.Settings().SetTheme(&adminTheme{})
	mainWin = a.NewWindow("Neon Admin Panel – pathl.pl")
	mainWin.Resize(fyne.NewSize(1200, 800))
	buildConnectScreen(a)
	mainWin.ShowAndRun()
}

func buildConnectScreen(a fyne.App) {
	title := canvas.NewText("NEON ADMIN PANEL", color.NRGBA{R: 0, G: 200, B: 150, A: 255})
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 26
	urlEntry := widget.NewEntry()
	urlEntry.SetText(serverURL)
	tcpEntry := widget.NewEntry()
	tcpEntry.SetText(tcpAddr)
	tokenEntry := widget.NewPasswordEntry()
	tokenEntry.SetText(adminToken)
	errLbl := widget.NewLabel("")
	connectBtn := widget.NewButton("Polacz z serwerem", func() {
		serverURL = strings.TrimRight(urlEntry.Text, "/")
		tcpAddr = tcpEntry.Text
		adminToken = tokenEntry.Text
		errLbl.SetText("Sprawdzam polaczenie...")
		go func() {
			_, err := fetchGames()
			if err != nil {
				fyne.Do(func() {
					errLbl.SetText("Blad: " + err.Error())
				})
				return
			}
			fyne.Do(func() {
				errLbl.SetText("")
				buildMainScreen(a)
			})
		}()
	})
	form := container.NewVBox(
		container.NewCenter(title),
		widget.NewSeparator(),
		widget.NewLabel("URL serwera HTTP:"), urlEntry,
		widget.NewLabel("Adres TCP (pobieranie):"), tcpEntry,
		widget.NewLabel("Token admina:"), tokenEntry,
		connectBtn, errLbl,
	)
	mainWin.SetContent(container.NewCenter(container.NewGridWrap(fyne.NewSize(420, 380), form)))
}

func buildMainScreen(a fyne.App) {
	var games []GameInfo
	var selectedGame *GameInfo

	detailName := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	mainExeLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})

	updateMainExeLabel := func() {
		if selectedGame == nil {
			mainExeLabel.SetText("")
			return
		}
		winExe := selectedGame.MainExeWindows
		if winExe == "" {
			winExe = "brak"
		}
		linExe := selectedGame.MainExeLinux
		if linExe == "" {
			linExe = "brak"
		}
		mainExeLabel.SetText(fmt.Sprintf("Wykonywalny Windows: %s\nWykonywalny Linux: %s", winExe, linExe))
	}
	detailVersion := widget.NewEntry()
	detailVersion.SetPlaceHolder("Wersja (np. 1.0.0)")
	detailDisplayName := widget.NewEntry()
	detailDisplayName.SetPlaceHolder("Wyswietlana nazwa")
	categorySelect := widget.NewSelect([]string{"game", "program", "tool", "library", "include", "cli", "other"}, nil)
	categorySelect.SetSelected("game")
	detailDesc := widget.NewMultiLineEntry()
	detailDesc.SetPlaceHolder("Opis gry...")
	detailDesc.SetMinRowsVisible(3)
	uploadProgress := widget.NewProgressBar()
	uploadStatus := widget.NewLabel("")

	var selectedFilePath string

	// ---- Filtrowanie plików ----
	var filteredFiles []string
	var fileSearch *widget.Entry
	var platformFilter *widget.Select
	var applyFileFilter func()

	detailFiles := widget.NewList(
		func() int {
			return len(filteredFiles)
		},
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel(""), // ikona platformy
				widget.NewLabel(""), // gwiazdka exe
				widget.NewLabel(""), // nazwa pliku
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(filteredFiles) {
				return
			}
			box := o.(*fyne.Container)
			filePath := filteredFiles[i]
			icon, _ := platformIcon(filePath)
			exeStar := "  "
			isWin := selectedGame != nil && filePath == selectedGame.MainExeWindows
			isLin := selectedGame != nil && filePath == selectedGame.MainExeLinux
			if isWin && isLin {
				exeStar = "⭐🪟🐧"
			} else if isWin {
				exeStar = "⭐🪟"
			} else if isLin {
				exeStar = "⭐🐧"
			}
			box.Objects[0].(*widget.Label).SetText(icon)
			box.Objects[1].(*widget.Label).SetText(exeStar)
			box.Objects[2].(*widget.Label).SetText(filePath)
		},
	)
	detailFiles.OnSelected = func(id widget.ListItemID) {
		if id < len(filteredFiles) {
			selectedFilePath = filteredFiles[id]
		}
	}

	applyFileFilter = func() {
		if selectedGame == nil {
			filteredFiles = nil
			detailFiles.Refresh()
			return
		}
		query := strings.ToLower(strings.TrimSpace(fileSearch.Text))
		platform := platformFilter.Selected
		filteredFiles = nil
		for _, f := range selectedGame.Files {
			switch platform {
			case "🐧 Linux":
				if !strings.HasPrefix(f, "linux/") {
					continue
				}
			case "🪟 Windows":
				if !strings.HasPrefix(f, "windows/") {
					continue
				}
			case "📦 Wspolne":
				if strings.HasPrefix(f, "linux/") || strings.HasPrefix(f, "windows/") {
					continue
				}
			}
			if query != "" && !strings.Contains(strings.ToLower(f), query) {
				continue
			}
			filteredFiles = append(filteredFiles, f)
		}
		detailFiles.Refresh()
	}

	platformFilter = widget.NewSelect(
		[]string{"Wszystkie", "🐧 Linux", "🪟 Windows", "📦 Wspolne"},
		func(_ string) { applyFileFilter() },
	)
	platformFilter.SetSelected("Wszystkie")

	fileSearch = widget.NewEntry()
	fileSearch.SetPlaceHolder("🔍 Szukaj pliku...")
	fileSearch.OnChanged = func(_ string) { applyFileFilter() }

	gameList := widget.NewList(
		func() int { return len(games) },
		func() fyne.CanvasObject {
			return container.NewVBox(
				widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewLabel(""),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			box := o.(*fyne.Container)
			box.Objects[0].(*widget.Label).SetText(games[i].DisplayName)
			box.Objects[1].(*widget.Label).SetText(fmt.Sprintf("v%s | %.1f MB", games[i].Version, float64(games[i].SizeBytes)/1024/1024))
		},
	)

	lastRefreshLbl := widget.NewLabel("Nie odswiezono")

	refreshGames := func() {
		g, err := fetchGames()
		if err != nil {
			fyne.Do(func() {
				lastRefreshLbl.SetText("❌ Blad: " + err.Error())
			})
			return
		}
		now := time.Now().Format("15:04:05")
		fyne.Do(func() {
			games = g
			gameList.Refresh()
			if selectedGame != nil {
				for i := range games {
					if games[i].Name == selectedGame.Name {
						selectedGame = &games[i]
						detailDisplayName.SetText(selectedGame.DisplayName)
						detailVersion.SetText(selectedGame.Version)
						detailDesc.SetText(selectedGame.Description)
						if selectedGame.Category != "" {
							categorySelect.SetSelected(selectedGame.Category)
						} else {
							categorySelect.SetSelected("game")
						}
						updateMainExeLabel()
						applyFileFilter()
						break
					}
				}
			}
			lastRefreshLbl.SetText("🔄 Ostatni refresh: " + now)
		})
	}
	go refreshGames()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshGames()
		}
	}()

	gameList.OnSelected = func(id widget.ListItemID) {
		if id >= len(games) {
			return
		}
		selectedGame = &games[id]
		selectedFilePath = ""
		detailName.SetText("Gra: " + selectedGame.Name)
		detailDisplayName.SetText(selectedGame.DisplayName)
		detailVersion.SetText(selectedGame.Version)
		detailDesc.SetText(selectedGame.Description)
		if selectedGame.Category != "" {
			categorySelect.SetSelected(selectedGame.Category)
		} else {
			categorySelect.SetSelected("game")
		}
		updateMainExeLabel()
		fileSearch.SetText("")
		platformFilter.SetSelected("Wszystkie")
		applyFileFilter()
		uploadStatus.SetText("")
		uploadProgress.SetValue(0)
	}

	saveMetaBtn := widget.NewButton("💾 Zapisz metadane", func() {
		if selectedGame == nil {
			return
		}
		name := selectedGame.Name
		dispName := detailDisplayName.Text
		desc := detailDesc.Text
		ver := detailVersion.Text
		mainExeWin := selectedGame.MainExeWindows
		mainExeLin := selectedGame.MainExeLinux
		cat := categorySelect.Selected
		uploadStatus.SetText("Zapisywanie...")
		go func() {
			err := saveMeta(name, dispName, desc, ver, mainExeWin, mainExeLin, cat)
			if err != nil {
				fyne.Do(func() { uploadStatus.SetText("Blad: " + err.Error()) })
				return
			}
			refreshGames()
			fyne.Do(func() { uploadStatus.SetText("✅ Metadane zapisane") })
		}()
	})

	setMainExeWinBtn := widget.NewButton("⭐ Główny Windows", func() {
		if selectedGame == nil {
			uploadStatus.SetText("Wybierz gre z listy")
			return
		}
		if selectedFilePath == "" {
			uploadStatus.SetText("Zaznacz plik z listy ponizej")
			return
		}
		gameName := selectedGame.Name
		dispName := selectedGame.DisplayName
		desc := selectedGame.Description
		ver := selectedGame.Version
		exePath := selectedFilePath
		mainExeLin := selectedGame.MainExeLinux

		selectedGame.MainExeWindows = exePath
		detailFiles.Refresh()
		updateMainExeLabel()
		uploadStatus.SetText("Zapisywanie...")

		go func() {
			err := saveMeta(gameName, dispName, desc, ver, exePath, mainExeLin, selectedGame.Category)
			if err != nil {
				fyne.Do(func() { uploadStatus.SetText("Blad zapisu: " + err.Error()) })
				return
			}
			refreshGames()
			fyne.Do(func() { uploadStatus.SetText("⭐ Główny Windows: " + exePath) })
		}()
	})

	setMainExeLinBtn := widget.NewButton("⭐ Główny Linux", func() {
		if selectedGame == nil {
			uploadStatus.SetText("Wybierz gre z listy")
			return
		}
		if selectedFilePath == "" {
			uploadStatus.SetText("Zaznacz plik z listy ponizej")
			return
		}
		gameName := selectedGame.Name
		dispName := selectedGame.DisplayName
		desc := selectedGame.Description
		ver := selectedGame.Version
		exePath := selectedFilePath
		mainExeWin := selectedGame.MainExeWindows

		selectedGame.MainExeLinux = exePath
		detailFiles.Refresh()
		updateMainExeLabel()
		uploadStatus.SetText("Zapisywanie...")

		go func() {
			err := saveMeta(gameName, dispName, desc, ver, mainExeWin, exePath, selectedGame.Category)
			if err != nil {
				fyne.Do(func() { uploadStatus.SetText("Blad zapisu: " + err.Error()) })
				return
			}
			refreshGames()
			fyne.Do(func() { uploadStatus.SetText("⭐ Główny Linux: " + exePath) })
		}()
	})

	// ---- NOWE PRZYCISKI ----
	deleteFileBtn := widget.NewButton("🗑 Usun plik", func() {
		if selectedGame == nil || selectedFilePath == "" {
			uploadStatus.SetText("Zaznacz plik z listy")
			return
		}
		gameName := selectedGame.Name
		filePath := selectedFilePath
		dialog.ShowConfirm("Usun plik",
			fmt.Sprintf("Czy na pewno usunac '%s'?", filePath),
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					err := deleteFile(gameName, filePath)
					if err != nil {
						fyne.Do(func() { uploadStatus.SetText("Błąd usuwania: " + err.Error()) })
						return
					}
					fyne.Do(func() {
						uploadStatus.SetText("Usunieto: " + filePath)
						selectedFilePath = ""
					})
					refreshGames()
				}()
			}, mainWin)
	})

	switchPlatformBtn := widget.NewButton("🔀 Przenies do...", func() {
		if selectedGame == nil || selectedFilePath == "" {
			uploadStatus.SetText("Zaznacz plik z listy")
			return
		}
		platforms := []string{"🪟 Windows", "🐧 Linux", "📦 Wspolne"}
		sel := widget.NewSelect(platforms, nil)
		sel.SetSelected("🪟 Windows")
		d := dialog.NewCustomConfirm("Przenies plik", "Przenies", "Anuluj",
			container.NewVBox(
				widget.NewLabel(fmt.Sprintf("Plik: %s", selectedFilePath)),
				widget.NewLabel("Wybierz nowa platforme:"),
				sel,
			),
			func(move bool) {
				if !move {
					return
				}
				newPlatform := ""
				switch sel.Selected {
				case "🪟 Windows":
					newPlatform = "windows"
				case "🐧 Linux":
					newPlatform = "linux"
					// "Wspolne" -> ""
				}
				gameName := selectedGame.Name
				oldPath := selectedFilePath
				go func() {
					err := moveFile(gameName, oldPath, newPlatform)
					if err != nil {
						fyne.Do(func() { uploadStatus.SetText("Błąd przenoszenia: " + err.Error()) })
						return
					}
					fyne.Do(func() { uploadStatus.SetText("Przeniesiono plik") })
					refreshGames()
				}()
			}, mainWin)
		d.Show()
	})

	platformSelect := widget.NewSelect([]string{"🪟 Windows", "🐧 Linux", "📦 Wspolne (common)"}, nil)
	platformSelect.SetSelected("🪟 Windows")

	getPlatformPrefix := func() string {
		switch platformSelect.Selected {
		case "🪟 Windows":
			return "windows"
		case "🐧 Linux":
			return "linux"
		default:
			return ""
		}
	}

	uploadFileBtn := widget.NewButton("Wgraj plik", func() {
		if selectedGame == nil {
			uploadStatus.SetText("Wybierz gre z listy")
			return
		}
		showCustomPicker("Wgraj plik: Wybierz plik do wyslania", false, func(localPath string) {
			go func() {
				fyne.Do(func() {
					uploadProgress.SetValue(0)
				})
				destRel := filepath.Base(localPath)
				pref := getPlatformPrefix()
				if pref != "" {
					destRel = pref + "/" + destRel
				}
				if err := uploadFile(selectedGame.Name, localPath, destRel, uploadProgress, uploadStatus); err != nil {
					fyne.Do(func() {
						uploadStatus.SetText("Blad uploadu: " + err.Error())
					})
				}
				go refreshGames()
			}()
		})
	})

	uploadFolderBtn := widget.NewButton("Wgraj caly folder", func() {
		if selectedGame == nil {
			uploadStatus.SetText("Wybierz gre z listy")
			return
		}
		showCustomPicker("Wgraj folder: Wybierz folder do wyslania", true, func(localPath string) {
			go func() {
				fyne.Do(func() {
					uploadProgress.SetValue(0)
				})
				pref := getPlatformPrefix()
				if err := uploadFolder(selectedGame.Name, localPath, pref, uploadProgress, uploadStatus); err != nil {
					fyne.Do(func() {
						uploadStatus.SetText("Blad folderu: " + err.Error())
					})
				}
				go refreshGames()
			}()
		})
	})

	deleteGameBtn := widget.NewButton("🗑 Usun gre", func() {
		if selectedGame == nil {
			return
		}
		dialog.ShowConfirm("Usun gre",
			fmt.Sprintf("Usunac '%s'? Nieodwracalne!", selectedGame.Name),
			func(ok bool) {
				if !ok {
					return
				}
				deleteGame(selectedGame.Name)
				selectedGame = nil
				selectedFilePath = ""
				detailName.SetText("")
				detailFiles.Refresh()
				go refreshGames()
			}, mainWin)
	})

	newGameName := widget.NewEntry()
	newGameName.SetPlaceHolder("Nazwa katalogu (np. fps-game)")
	newGameBtn := widget.NewButton("➕ Nowa gra", func() {
		name := strings.TrimSpace(newGameName.Text)
		if name == "" {
			return
		}
		saveMeta(name, name, "", "1.0.0", "", "", "game")
		newGameName.SetText("")
		go refreshGames()
		uploadStatus.SetText("Gra '" + name + "' dodana – wgraj pliki")
	})

	// Prawa strona – szczegóły
	rightDetail := container.NewBorder(
		container.NewVBox(
			detailName,
			widget.NewForm(
				widget.NewFormItem("Wyswietlana nazwa:", detailDisplayName),
				widget.NewFormItem("Wersja:", detailVersion),
				widget.NewFormItem("Kategoria:", categorySelect),
			),
			detailDesc,
			container.NewGridWithColumns(2, saveMetaBtn, deleteGameBtn),
			widget.NewSeparator(),
			widget.NewLabelWithStyle("📁 Pliki gry:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("  🐧 Linux   🪟 Windows   📦 Wspolne   ⭐ = Glowny wykonywalny"),
			mainExeLabel,
			fileSearch,
			widget.NewForm(widget.NewFormItem("Filtr platformy:", platformFilter)),
		),
		container.NewVBox(
			widget.NewSeparator(),
			widget.NewForm(widget.NewFormItem("Platforma uploadu:", platformSelect)),
			container.NewGridWithColumns(2, uploadFileBtn, uploadFolderBtn),
			container.NewGridWithColumns(2, setMainExeWinBtn, setMainExeLinBtn),
			container.NewGridWithColumns(2, deleteFileBtn, switchPlatformBtn), // NOWE
			uploadProgress,
			uploadStatus,
		),
		nil, nil,
		detailFiles,
	)

	refreshBtn := widget.NewButton("🔄 Odswiez liste gier", func() { go refreshGames() })

	leftPanel := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Gry na serwerze", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			container.NewBorder(nil, nil, nil, newGameBtn, newGameName),
			refreshBtn,
			lastRefreshLbl,
			widget.NewSeparator(),
		),
		nil, nil, nil,
		gameList,
	)

	gamesTab := container.NewHSplit(leftPanel, rightDetail)
	gamesTab.SetOffset(0.3)

	// --- Speed test tab ---
	speedSizeSelect := widget.NewSelect([]string{"100 MB", "500 MB", "1 GB", "2 GB"}, nil)
	speedSizeSelect.SetSelected("1 GB")
	speedProgress := widget.NewProgressBar()
	speedStatus := widget.NewLabel("Gotowy do testu")
	speedStatus.Wrapping = fyne.TextWrapWord
	speedHistory := widget.NewMultiLineEntry()
	speedHistory.Disable()

	startSpeedBtn := widget.NewButton("Rozpocznij test predkosci", func() {
		sizeMap := map[string]int64{
			"100 MB": 100 << 20,
			"500 MB": 500 << 20,
			"1 GB":   1 << 30,
			"2 GB":   2 << 30,
		}
		size := sizeMap[speedSizeSelect.Selected]
		if size == 0 {
			size = 1 << 30
		}
		speedProgress.SetValue(0)
		go func() {
			runSpeedTest(size, speedProgress, speedStatus)
			ts := time.Now().Format("15:04:05")
			fyne.Do(func() {
				speedHistory.SetText(fmt.Sprintf("[%s] %s: %s\n", ts, speedSizeSelect.Selected, speedStatus.Text) + speedHistory.Text)
			})
		}()
	})

	networkTab := container.NewVBox(
		widget.NewLabelWithStyle("Test predkosci pobierania przez TCP", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewForm(widget.NewFormItem("Rozmiar testu:", speedSizeSelect)),
		startSpeedBtn,
		widget.NewSeparator(),
		speedProgress,
		speedStatus,
		widget.NewSeparator(),
		widget.NewLabel("Historia:"),
		speedHistory,
	)

	hdr := canvas.NewText("NEON ADMIN  |  "+serverURL, color.NRGBA{R: 0, G: 200, B: 150, A: 255})
	hdr.TextStyle = fyne.TextStyle{Bold: true}
	hdr.TextSize = 15

	tabs := container.NewAppTabs(
		container.NewTabItem("Gry", gamesTab),
		container.NewTabItem("Test sieci", networkTab),
	)
	mainWin.SetContent(container.NewBorder(
		container.NewVBox(
			container.NewBorder(nil, nil, container.NewPadded(hdr),
				widget.NewButton("Rozlacz", func() { buildConnectScreen(a) }),
			),
			widget.NewSeparator(),
		),
		nil, nil, nil, tabs,
	))
}

func showCustomPicker(title string, folderMode bool, callback func(string)) {
	currentPath, err := os.Getwd()
	if err != nil {
		currentPath = "/"
	}

	type FileItem struct {
		Name  string
		IsDir bool
		Path  string
	}
	var items []FileItem
	var selectedItem *FileItem

	pathEntry := widget.NewEntry()
	pathEntry.SetText(currentPath)

	list := widget.NewList(
		func() int { return len(items) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel(""),
				widget.NewLabel(""),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(items) {
				return
			}
			box := o.(*fyne.Container)
			iconLbl := box.Objects[0].(*widget.Label)
			nameLbl := box.Objects[1].(*widget.Label)

			item := items[i]
			if item.Name == ".." {
				iconLbl.SetText("⬅️")
				nameLbl.SetText("[W gore]")
			} else if item.IsDir {
				iconLbl.SetText("📁")
				nameLbl.SetText(item.Name)
			} else {
				iconLbl.SetText("📄")
				nameLbl.SetText(item.Name)
			}
		},
	)

	var popup *widget.PopUp

	loadDir := func(dir string) {
		dir = filepath.Clean(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			dialog.ShowError(err, mainWin)
			return
		}

		currentPath = dir
		pathEntry.SetText(dir)
		selectedItem = nil

		var newItems []FileItem
		parent := filepath.Dir(dir)
		if parent != dir {
			newItems = append(newItems, FileItem{Name: "..", IsDir: true, Path: parent})
		}

		for _, e := range entries {
			if e.IsDir() {
				newItems = append(newItems, FileItem{
					Name:  e.Name(),
					IsDir: true,
					Path:  filepath.Join(dir, e.Name()),
				})
			}
		}
		for _, e := range entries {
			if !e.IsDir() {
				newItems = append(newItems, FileItem{
					Name:  e.Name(),
					IsDir: false,
					Path:  filepath.Join(dir, e.Name()),
				})
			}
		}

		items = newItems
		list.Refresh()
		list.UnselectAll()
	}

	var lastClick time.Time
	var lastID widget.ListItemID = -1

	list.OnSelected = func(id widget.ListItemID) {
		if id >= len(items) {
			return
		}
		selectedItem = &items[id]

		now := time.Now()
		if lastID == id && now.Sub(lastClick) < 300*time.Millisecond {
			if selectedItem.IsDir {
				loadDir(selectedItem.Path)
			}
			lastID = -1
			return
		}
		lastClick = now
		lastID = id
	}

	list.OnUnselected = func(id widget.ListItemID) {
		selectedItem = nil
	}

	goBtn := widget.NewButton("Wejdz / Idz", func() {
		if selectedItem != nil && selectedItem.IsDir {
			loadDir(selectedItem.Path)
		} else {
			loadDir(pathEntry.Text)
		}
	})

	topBar := container.NewBorder(nil, nil, nil, goBtn, pathEntry)

	var bottomBar *fyne.Container

	cancelBtn := widget.NewButton("Anuluj", func() {
		popup.Hide()
	})

	enterFolderBtn := widget.NewButton("Wejdz do folderu", func() {
		if selectedItem != nil && selectedItem.IsDir {
			loadDir(selectedItem.Path)
		} else {
			dialog.ShowInformation("Informacja", "Zaznacz folder z listy, aby do niego wejsc", mainWin)
		}
	})

	if folderMode {
		selectBtn := widget.NewButton("Wgraj ten folder", func() {
			popup.Hide()
			callback(currentPath)
		})
		selectSelectedBtn := widget.NewButton("Wgraj zaznaczony folder", func() {
			if selectedItem != nil && selectedItem.IsDir && selectedItem.Name != ".." {
				popup.Hide()
				callback(selectedItem.Path)
			} else {
				dialog.ShowInformation("Informacja", "Zaznacz prawidlowy folder z listy lub kliknij 'Wgraj ten folder'", mainWin)
			}
		})
		bottomBar = container.NewGridWithColumns(4, selectBtn, selectSelectedBtn, enterFolderBtn, cancelBtn)
	} else {
		selectBtn := widget.NewButton("Wgraj zaznaczony plik", func() {
			if selectedItem != nil && !selectedItem.IsDir {
				popup.Hide()
				callback(selectedItem.Path)
			} else {
				dialog.ShowInformation("Informacja", "Wybierz plik z listy, aby go wgrac", mainWin)
			}
		})
		bottomBar = container.NewGridWithColumns(3, selectBtn, enterFolderBtn, cancelBtn)
	}

	pickerLayout := container.NewBorder(
		topBar,
		bottomBar,
		nil, nil,
		container.NewGridWrap(fyne.NewSize(580, 360), list),
	)

	popup = widget.NewModalPopUp(pickerLayout, mainWin.Canvas())
	popup.Resize(fyne.NewSize(600, 450))
	loadDir(currentPath)
	popup.Show()
}

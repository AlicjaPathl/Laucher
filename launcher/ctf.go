package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type CTFTask struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TargetIP    string `json:"target_ip"`
	XPReward    string `json:"xp_reward"`
	Difficulty  string `json:"difficulty"`
}

func makeCTFTab(window fyne.Window) fyne.CanvasObject {
	titleLabel := widget.NewLabelWithStyle("STREFA CTF (CAPTURE THE FLAG)", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	// Lista zadań (pobierana dynamicznie z serwera)
	var tasks []CTFTask

	taskTitle := widget.NewLabelWithStyle("Wybierz operację z panelu", fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Italic: true})
	taskIP := widget.NewLabel("")
	taskXP := widget.NewLabel("")
	taskDesc := widget.NewLabel("")
	taskDesc.Wrapping = fyne.TextWrapWord

	flagInput := widget.NewEntry()
	flagInput.SetPlaceHolder("Wklej zdobytą flagę (np. CTF{n30n_l4unch3r_h4ck})")
	flagInput.Hide()

	submitBtn := widget.NewButton("🔓 Zatwierdź Flagę", nil)
	submitBtn.Importance = widget.SuccessImportance
	submitBtn.Hide()

	var selectedTask *CTFTask

	taskListWidget := widget.NewList(
		func() int { return len(tasks) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			o.(*widget.Label).SetText(fmt.Sprintf("[%s] %s", tasks[i].Difficulty, tasks[i].Title))
		},
	)

	refreshCTFData := func() {
		resp, err := http.Get(serverURL + "/ctf/tasks")
		if err != nil {
			// Fallback (makieta jeśli serwer jeszcze nie stoi)
			tasks = []CTFTask{
				{ID: "1", Title: "Infiltracja portu 80", Description: "Serwer HTTP na tej maszynie podatny jest na Command Injection. Wyciągnij flagę z pliku /root/flag.txt", TargetIP: "10.10.120.45", XPReward: "250 XP", Difficulty: "Łatwy"},
				{ID: "2", Title: "Przełamanie bazy danych", Description: "Odnajdź ukryty panel administratora i za pomocą SQL Injection uzyskaj dostęp do tabeli flag.", TargetIP: "10.10.120.89", XPReward: "600 XP", Difficulty: "Średni"},
			}
			return
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&tasks)
		taskListWidget.Refresh()
	}

	taskListWidget.OnSelected = func(id widget.ListItemID) {
		selectedTask = &tasks[id]
		taskTitle.SetText("🎯 Zadanie: " + selectedTask.Title)
		taskIP.SetText("🖥️ Adres IP celu: " + selectedTask.TargetIP)
		taskXP.SetText("💎 Nagroda: " + selectedTask.XPReward)
		taskDesc.SetText(selectedTask.Description)

		flagInput.Show()
		submitBtn.Show()
	}

	submitBtn.OnTapped = func() {
		if selectedTask == nil || flagInput.Text == "" {
			return
		}

		// Generowanie hashu SHA-512 z flagi wpisanej przez gracza
		hashedFlag := hashFlag(flagInput.Text)

		// Wysłanie zapytania POST do backendu
		payload := ctfSubmitRequest{
			Username: currentUser,
			TaskID:   selectedTask.ID,
			FlagHash: hashedFlag,
		}

		res, err := apiPost("/ctf/submit", payload)
		if err != nil {
			dialog.ShowError(fmt.Errorf("Błędna flaga lub błąd systemu! Spróbuj ponownie."), window)
			return
		}

		// Backend powinien odpowiedzieć wiadomością sukcesu i nowym stanem punktów XP
		newXP := res["current_xp"]
		dialog.ShowInformation("SYSTEM ZHACKOWANY!", fmt.Sprintf("Dobra robota, agencie! Flaga poprawna.\nZyskujesz %s.\nTwój obecny stan to: %s XP", selectedTask.XPReward, newXP), window)

		flagInput.SetText("")
		refreshCTFData()
	}

	// Odśwież dane na starcie strefy CTF
	refreshCTFData()

	leftPanel := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Dostępne Cele", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewButton("🔄 Odśwież sieć", refreshCTFData),
			widget.NewSeparator(),
		),
		nil, nil, nil,
		taskListWidget,
	)

	rightPanel := container.NewBorder(
		container.NewVBox(
			taskTitle,
			widget.NewSeparator(),
			taskIP,
			taskXP,
			widget.NewSeparator(),
		),
		container.NewVBox(
			widget.NewSeparator(),
			flagInput,
			submitBtn,
		),
		nil, nil,
		container.NewScroll(taskDesc),
	)

	splitLayout := container.NewHSplit(leftPanel, rightPanel)
	splitLayout.SetOffset(0.35)

	return container.NewBorder(titleLabel, nil, nil, nil, splitLayout)
}

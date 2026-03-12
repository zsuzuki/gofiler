package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var confirmStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
var currentLineStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("159")).
	Foreground(lipgloss.Color("236"))

type entry struct {
	name    string
	path    string
	isDir   bool
	size    int64
	modTime time.Time
}

type pendingAction struct {
	label string
	code  string
}

type model struct {
	cwd           string
	entries       []entry
	visible       []entry
	cursor        int
	offset        int
	height        int
	message       string
	marked        map[string]bool
	pending       *pendingAction
	renaming      bool
	renameSrc     string
	renameOld     string
	renameInput   string
	renameCursor  int
	renameTarget  string
	filterQuery   string
	filtering     bool
	filterInput   string
	filterCursor  int
	sizeOp        string
	sizeBytes     int64
	sizeFiltering bool
	sizeInput     string
	sizeCursor    int
	sizeInputOp   string
	sortKey       string
	sortDesc      bool
	helpVisible   bool
	quitting      bool
}

func newModel() model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	m := model{
		cwd:      cwd,
		marked:   map[string]bool{},
		sortKey:  "name",
		sortDesc: false,
	}
	m.reload()
	return m
}

func (m *model) reload() {
	entries, err := os.ReadDir(m.cwd)
	if err != nil {
		m.message = "ディレクトリ読み込み失敗: " + err.Error()
		m.entries = nil
		m.cursor = 0
		m.offset = 0
		return
	}

	list := make([]entry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, entry{
			name:    e.Name(),
			path:    filepath.Join(m.cwd, e.Name()),
			isDir:   e.IsDir(),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}

	m.entries = list
	m.sortEntries()
	m.applyFilter()
}

func (m *model) sortEntries() {
	sort.Slice(m.entries, func(i, j int) bool {
		left := m.entries[i]
		right := m.entries[j]
		if left.isDir != right.isDir {
			return left.isDir
		}

		var cmp int
		switch m.sortKey {
		case "size":
			if left.size != right.size {
				if left.size < right.size {
					cmp = -1
				} else {
					cmp = 1
				}
			} else {
				cmp = strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
			}
		case "time":
			if !left.modTime.Equal(right.modTime) {
				if left.modTime.Before(right.modTime) {
					cmp = -1
				} else {
					cmp = 1
				}
			} else {
				cmp = strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
			}
		default:
			cmp = strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
		}

		if m.sortDesc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func (m *model) sortLabel() string {
	dir := "asc"
	if m.sortDesc {
		dir = "desc"
	}
	return m.sortKey + " " + dir
}

func nextSortKey(current string) string {
	switch current {
	case "name":
		return "size"
	case "size":
		return "time"
	default:
		return "name"
	}
}

func (m *model) applyFilter() {
	q := strings.ToLower(m.filterQuery)
	filtered := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if q != "" && !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		if m.sizeOp != "" {
			if e.isDir {
				continue
			}
			if m.sizeOp == "<=" && e.size > m.sizeBytes {
				continue
			}
			if m.sizeOp == ">=" && e.size < m.sizeBytes {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	m.visible = filtered
	if len(m.visible) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureCursorVisible()
}

func (m *model) clearFilter() {
	m.filterQuery = ""
	m.filterInput = ""
	m.filterCursor = 0
	m.filtering = false
	m.sizeOp = ""
	m.sizeBytes = 0
	m.sizeFiltering = false
	m.sizeInput = ""
	m.sizeCursor = 0
	m.sizeInputOp = ""
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) pageSize() int {
	base := m.height - 8
	if base < 5 {
		return 10
	}
	return base
}

func (m *model) ensureCursorVisible() {
	p := m.pageSize()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+p {
		m.offset = m.cursor - p + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	maxOffset := len(m.visible) - p
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
}

func (m *model) actionDescription(code string) string {
	switch code {
	case "up":
		return "カーソルを上に移動"
	case "down":
		return "カーソルを下に移動"
	case "left":
		return "親ディレクトリへ移動"
	case "right":
		return "選択中ディレクトリへ移動"
	case "pgup":
		return "1ページ上へスクロール"
	case "pgdown":
		return "1ページ下へスクロール"
	case "space":
		return "選択/解除を切り替え"
	case "delete":
		return "選択対象を削除"
	case "move":
		return "選択対象をカレントディレクトリへ移動"
	case "copy":
		return "選択対象をカレントディレクトリへコピー"
	case "rename":
		return "入力名でリネーム実行"
	case "quit":
		return "終了"
	default:
		return code
	}
}

func (m *model) beginAction(code string) {
	m.pending = &pendingAction{label: m.actionDescription(code), code: code}
}

func (m *model) applyAction(code string) tea.Cmd {
	switch code {
	case "up":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		if m.cursor > 0 {
			m.cursor--
			m.ensureCursorVisible()
		}
		m.message = "カーソルを上に移動"
	case "down":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			m.ensureCursorVisible()
		}
		m.message = "カーソルを下に移動"
	case "left":
		parent := filepath.Dir(m.cwd)
		if parent == m.cwd {
			m.message = "これ以上上へは移動できません"
			return nil
		}
		m.cwd = parent
		m.clearFilter()
		m.cursor = 0
		m.offset = 0
		m.reload()
		m.message = "親ディレクトリへ移動"
	case "right":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		current := m.visible[m.cursor]
		if !current.isDir {
			if m.marked[current.path] {
				delete(m.marked, current.path)
				m.message = "選択解除: " + current.name
			} else {
				m.marked[current.path] = true
				m.message = "選択: " + current.name
			}
			return nil
		}
		m.cwd = current.path
		m.clearFilter()
		m.cursor = 0
		m.offset = 0
		m.reload()
		m.message = "ディレクトリへ移動"
	case "pgup":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		step := m.pageSize()
		m.cursor -= step
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureCursorVisible()
		m.message = "1ページ上へ移動"
	case "pgdown":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		step := m.pageSize()
		m.cursor += step
		if m.cursor >= len(m.visible) {
			m.cursor = len(m.visible) - 1
		}
		m.ensureCursorVisible()
		m.message = "1ページ下へ移動"
	case "space":
		if len(m.visible) == 0 {
			m.message = "対象がありません"
			return nil
		}
		current := m.visible[m.cursor]
		if m.marked[current.path] {
			delete(m.marked, current.path)
			m.message = "選択解除: " + current.name
		} else {
			m.marked[current.path] = true
			m.message = "選択: " + current.name
		}
	case "delete":
		targets := m.selectedTargets()
		if len(targets) == 0 {
			m.message = "削除対象がありません"
			return nil
		}
		deleted := 0
		for _, t := range targets {
			if err := os.RemoveAll(t.path); err != nil {
				m.message = "削除失敗: " + err.Error()
				continue
			}
			delete(m.marked, t.path)
			deleted++
		}
		m.reload()
		m.message = fmt.Sprintf("%d件削除しました", deleted)
	case "move":
		targets := m.selectedTargets()
		if len(targets) == 0 {
			m.message = "移動対象がありません"
			return nil
		}
		moved := 0
		for _, t := range targets {
			dst := filepath.Join(m.cwd, filepath.Base(t.path))
			if dst == t.path {
				continue
			}
			if _, err := os.Stat(dst); err == nil {
				m.message = "移動先に同名ファイルが存在: " + filepath.Base(t.path)
				continue
			}
			if err := movePath(t.path, dst); err != nil {
				m.message = "移動失敗: " + err.Error()
				continue
			}
			delete(m.marked, t.path)
			moved++
		}
		m.reload()
		m.message = fmt.Sprintf("%d件移動しました", moved)
	case "copy":
		targets := m.selectedTargets()
		if len(targets) == 0 {
			m.message = "コピー対象がありません"
			return nil
		}
		copied := 0
		for _, t := range targets {
			dst := filepath.Join(m.cwd, filepath.Base(t.path))
			if dst == t.path {
				continue
			}
			if _, err := os.Stat(dst); err == nil {
				m.message = "コピー先に同名ファイルが存在: " + filepath.Base(t.path)
				continue
			}
			if err := copyPath(t.path, dst); err != nil {
				m.message = "コピー失敗: " + err.Error()
				continue
			}
			copied++
		}
		m.reload()
		m.message = fmt.Sprintf("%d件コピーしました", copied)
	case "rename":
		if m.renameSrc == "" || m.renameTarget == "" {
			m.message = "リネーム情報が不正です"
			return nil
		}
		if err := os.Rename(m.renameSrc, m.renameTarget); err != nil {
			m.message = "リネーム失敗: " + err.Error()
			return nil
		}
		if m.marked[m.renameSrc] {
			delete(m.marked, m.renameSrc)
			m.marked[m.renameTarget] = true
		}
		renamed := m.renameTarget
		m.renameSrc = ""
		m.renameOld = ""
		m.renameInput = ""
		m.renameCursor = 0
		m.renameTarget = ""
		m.reload()
		for i, e := range m.visible {
			if e.path == renamed {
				m.cursor = i
				m.ensureCursorVisible()
				break
			}
		}
		m.message = "リネームしました"
	case "quit":
		m.quitting = true
		return tea.Quit
	}

	return nil
}

func (m *model) selectedTargets() []entry {
	targets := make([]entry, 0, len(m.marked))
	for path, marked := range m.marked {
		if !marked {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			delete(m.marked, path)
			continue
		}
		targets = append(targets, entry{
			name:    filepath.Base(path),
			path:    path,
			isDir:   info.IsDir(),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		return strings.ToLower(targets[i].path) < strings.ToLower(targets[j].path)
	})
	if len(targets) == 0 && len(m.visible) > 0 && m.cursor < len(m.visible) {
		targets = append(targets, m.visible[m.cursor])
	}
	return targets
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyMsg:
		if m.sizeFiltering {
			switch msg.String() {
			case "esc":
				m.sizeFiltering = false
				m.sizeInput = ""
				m.sizeCursor = 0
				m.sizeInputOp = ""
				m.message = "サイズフィルター入力をキャンセルしました"
				return m, nil
			case "enter":
				input := strings.TrimSpace(m.sizeInput)
				if input == "" {
					m.sizeOp = ""
					m.sizeBytes = 0
					m.sizeFiltering = false
					m.sizeInput = ""
					m.sizeCursor = 0
					m.sizeInputOp = ""
					m.cursor = 0
					m.offset = 0
					m.applyFilter()
					m.message = "サイズフィルター解除"
					return m, nil
				}
				sizeBytes, err := parseSizeInput(input)
				if err != nil {
					m.message = "サイズ指定エラー: " + err.Error()
					return m, nil
				}
				m.sizeOp = m.sizeInputOp
				m.sizeBytes = sizeBytes
				m.sizeFiltering = false
				m.sizeInput = ""
				m.sizeCursor = 0
				m.sizeInputOp = ""
				m.cursor = 0
				m.offset = 0
				m.applyFilter()
				m.message = fmt.Sprintf("サイズフィルター適用: %s %s", m.sizeOp, humanSize(m.sizeBytes))
				return m, nil
			case "backspace", "ctrl+h":
				r := []rune(m.sizeInput)
				if m.sizeCursor > 0 && len(r) > 0 {
					r = append(r[:m.sizeCursor-1], r[m.sizeCursor:]...)
					m.sizeCursor--
					m.sizeInput = string(r)
				}
				return m, nil
			case "delete":
				r := []rune(m.sizeInput)
				if m.sizeCursor < len(r) {
					r = append(r[:m.sizeCursor], r[m.sizeCursor+1:]...)
					m.sizeInput = string(r)
				}
				return m, nil
			case "left":
				if m.sizeCursor > 0 {
					m.sizeCursor--
				}
				return m, nil
			case "right":
				if m.sizeCursor < len([]rune(m.sizeInput)) {
					m.sizeCursor++
				}
				return m, nil
			case "home", "ctrl+a":
				m.sizeCursor = 0
				return m, nil
			case "end", "ctrl+e":
				m.sizeCursor = len([]rune(m.sizeInput))
				return m, nil
			default:
				if len(msg.Runes) > 0 {
					r := []rune(m.sizeInput)
					in := msg.Runes
					newRunes := make([]rune, 0, len(r)+len(in))
					newRunes = append(newRunes, r[:m.sizeCursor]...)
					newRunes = append(newRunes, in...)
					newRunes = append(newRunes, r[m.sizeCursor:]...)
					m.sizeInput = string(newRunes)
					m.sizeCursor += len(in)
				}
				return m, nil
			}
		}

		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filterInput = ""
				m.filterCursor = 0
				m.message = "フィルター入力をキャンセルしました"
				return m, nil
			case "enter":
				m.filterQuery = strings.TrimSpace(m.filterInput)
				m.filtering = false
				m.filterInput = ""
				m.filterCursor = 0
				m.cursor = 0
				m.offset = 0
				m.applyFilter()
				if m.filterQuery == "" {
					m.message = "フィルター解除: 全表示"
				} else {
					m.message = "フィルター適用: " + m.filterQuery
				}
				return m, nil
			case "backspace", "ctrl+h":
				r := []rune(m.filterInput)
				if m.filterCursor > 0 && len(r) > 0 {
					r = append(r[:m.filterCursor-1], r[m.filterCursor:]...)
					m.filterCursor--
					m.filterInput = string(r)
				}
				return m, nil
			case "delete":
				r := []rune(m.filterInput)
				if m.filterCursor < len(r) {
					r = append(r[:m.filterCursor], r[m.filterCursor+1:]...)
					m.filterInput = string(r)
				}
				return m, nil
			case "left":
				if m.filterCursor > 0 {
					m.filterCursor--
				}
				return m, nil
			case "right":
				if m.filterCursor < len([]rune(m.filterInput)) {
					m.filterCursor++
				}
				return m, nil
			case "home", "ctrl+a":
				m.filterCursor = 0
				return m, nil
			case "end", "ctrl+e":
				m.filterCursor = len([]rune(m.filterInput))
				return m, nil
			default:
				if len(msg.Runes) > 0 {
					r := []rune(m.filterInput)
					in := msg.Runes
					newRunes := make([]rune, 0, len(r)+len(in))
					newRunes = append(newRunes, r[:m.filterCursor]...)
					newRunes = append(newRunes, in...)
					newRunes = append(newRunes, r[m.filterCursor:]...)
					m.filterInput = string(newRunes)
					m.filterCursor += len(in)
				}
				return m, nil
			}
		}

		if m.renaming {
			switch msg.String() {
			case "esc":
				m.renaming = false
				m.renameSrc = ""
				m.renameOld = ""
				m.renameInput = ""
				m.renameCursor = 0
				m.renameTarget = ""
				m.message = "リネームをキャンセルしました"
				return m, nil
			case "enter":
				newName := strings.TrimSpace(m.renameInput)
				if newName == "" {
					m.message = "名前を入力してください"
					return m, nil
				}
				if newName == "." || newName == ".." || strings.ContainsRune(newName, filepath.Separator) {
					m.message = "無効な名前です"
					return m, nil
				}
				dst := filepath.Join(filepath.Dir(m.renameSrc), newName)
				if dst == m.renameSrc {
					m.renaming = false
					m.renameSrc = ""
					m.renameOld = ""
					m.renameInput = ""
					m.renameCursor = 0
					m.renameTarget = ""
					m.message = "同名のため変更なし"
					return m, nil
				}
				if _, err := os.Stat(dst); err == nil {
					m.message = "同名のファイル/ディレクトリが存在します"
					return m, nil
				}
				m.renaming = false
				m.renameTarget = dst
				m.pending = &pendingAction{
					label: fmt.Sprintf("リネーム: %s -> %s", m.renameOld, newName),
					code:  "rename",
				}
				m.message = "リネーム確認待ち"
				return m, nil
			case "backspace", "ctrl+h":
				r := []rune(m.renameInput)
				if m.renameCursor > 0 && len(r) > 0 {
					r = append(r[:m.renameCursor-1], r[m.renameCursor:]...)
					m.renameCursor--
					m.renameInput = string(r)
				}
				return m, nil
			case "delete":
				r := []rune(m.renameInput)
				if m.renameCursor < len(r) {
					r = append(r[:m.renameCursor], r[m.renameCursor+1:]...)
					m.renameInput = string(r)
				}
				return m, nil
			case "left":
				if m.renameCursor > 0 {
					m.renameCursor--
				}
				return m, nil
			case "right":
				if m.renameCursor < len([]rune(m.renameInput)) {
					m.renameCursor++
				}
				return m, nil
			case "home", "ctrl+a":
				m.renameCursor = 0
				return m, nil
			case "end", "ctrl+e":
				m.renameCursor = len([]rune(m.renameInput))
				return m, nil
			case "alt+b":
				r := []rune(m.renameInput)
				for m.renameCursor > 0 && r[m.renameCursor-1] == ' ' {
					m.renameCursor--
				}
				for m.renameCursor > 0 && r[m.renameCursor-1] != ' ' {
					m.renameCursor--
				}
				return m, nil
			case "alt+f":
				r := []rune(m.renameInput)
				for m.renameCursor < len(r) && r[m.renameCursor] == ' ' {
					m.renameCursor++
				}
				for m.renameCursor < len(r) && r[m.renameCursor] != ' ' {
					m.renameCursor++
				}
				return m, nil
			default:
				if len(msg.Runes) > 0 {
					r := []rune(m.renameInput)
					in := msg.Runes
					newRunes := make([]rune, 0, len(r)+len(in))
					newRunes = append(newRunes, r[:m.renameCursor]...)
					newRunes = append(newRunes, in...)
					newRunes = append(newRunes, r[m.renameCursor:]...)
					m.renameInput = string(newRunes)
					m.renameCursor += len(in)
				}
				return m, nil
			}
		}

		if m.pending != nil {
			switch msg.String() {
			case "y", "Y":
				code := m.pending.code
				m.pending = nil
				return m, m.applyAction(code)
			case "n", "N", "esc":
				m.pending = nil
				m.message = "キャンセルしました"
				return m, nil
			default:
				return m, nil
			}
		}

		switch msg.String() {
		case "?":
			m.helpVisible = !m.helpVisible
			if m.helpVisible {
				m.message = "キーバインド一覧を表示"
			} else {
				m.message = "キーバインド一覧を閉じました"
			}
			return m, nil
		case "up":
			return m, m.applyAction("up")
		case "down":
			return m, m.applyAction("down")
		case "left":
			return m, m.applyAction("left")
		case "right", "enter":
			return m, m.applyAction("right")
		case "pgup":
			return m, m.applyAction("pgup")
		case "pgdown":
			return m, m.applyAction("pgdown")
		case " ":
			return m, m.applyAction("space")
		case "d":
			m.beginAction("delete")
		case "m":
			m.beginAction("move")
		case "c":
			m.beginAction("copy")
		case "r":
			if len(m.visible) == 0 {
				m.message = "対象がありません"
				return m, nil
			}
			current := m.visible[m.cursor]
			m.renaming = true
			m.renameSrc = current.path
			m.renameOld = current.name
			m.renameInput = current.name
			m.renameCursor = len([]rune(current.name))
			m.renameTarget = ""
			m.message = "新しい名前を入力してください"
			return m, nil
		case "f":
			m.filtering = true
			m.filterInput = m.filterQuery
			m.filterCursor = len([]rune(m.filterInput))
			m.message = "フィルター文字列を入力してください"
			return m, nil
		case "s":
			m.sizeFiltering = true
			m.sizeInput = ""
			m.sizeCursor = 0
			m.sizeInputOp = "<="
			m.message = "サイズ上限を入力してください (例: 500KB, 10MB)"
			return m, nil
		case "S":
			m.sizeFiltering = true
			m.sizeInput = ""
			m.sizeCursor = 0
			m.sizeInputOp = ">="
			m.message = "サイズ下限を入力してください (例: 500KB, 10MB)"
			return m, nil
		case "x":
			if m.filterQuery == "" && m.sizeOp == "" {
				m.message = "解除するフィルターはありません"
				return m, nil
			}
			m.clearFilter()
			m.cursor = 0
			m.offset = 0
			m.applyFilter()
			m.message = "フィルターをすべて解除しました"
			return m, nil
		case "o":
			m.sortKey = nextSortKey(m.sortKey)
			m.sortEntries()
			m.applyFilter()
			m.message = "ソート変更: " + m.sortLabel()
			return m, nil
		case "O":
			m.sortDesc = !m.sortDesc
			m.sortEntries()
			m.applyFilter()
			m.message = "ソート方向変更: " + m.sortLabel()
			return m, nil
		case "R":
			m.reload()
			m.message = "再読み込みしました"
			return m, nil
		case "q":
			m.beginAction("quit")
		}
	}

	return m, nil
}

func humanSize(size int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(size)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d%s", int64(v), units[i])
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

func (m model) View() string {
	if m.quitting {
		return "終了します\n"
	}

	var b strings.Builder
	b.WriteString("Current: " + m.cwd + "\n")
	if m.filterQuery == "" {
		b.WriteString("Filter: (none)\n")
	} else {
		b.WriteString("Filter: " + m.filterQuery + "\n")
	}
	if m.sizeOp == "" {
		b.WriteString("Size: (none)\n")
	} else {
		b.WriteString(fmt.Sprintf("Size: %s %s\n", m.sizeOp, humanSize(m.sizeBytes)))
	}
	b.WriteString("Sort: " + m.sortLabel() + "\n")
	b.WriteString("------------------------------------------------------------\n")
	b.WriteString("  Sel Name                             Size      Modified\n")

	p := m.pageSize()
	start := m.offset
	end := start + p
	if end > len(m.visible) {
		end = len(m.visible)
	}

	for i := start; i < end; i++ {
		e := m.visible[i]
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		mark := " "
		if m.marked[e.path] {
			mark = "*"
		}

		name := e.name
		if e.isDir {
			name += "/"
		}
		if len([]rune(name)) > 32 {
			r := []rune(name)
			name = string(r[:31]) + "…"
		}

		sizeText := "-"
		if !e.isDir {
			sizeText = humanSize(e.size)
		}

		line := fmt.Sprintf("%s [%s] %-32s %8s   %s\n",
			cursor,
			mark,
			name,
			sizeText,
			e.modTime.Format("2006-01-02 15:04:05"),
		)
		if i == m.cursor {
			line = currentLineStyle.Render(strings.TrimRight(line, "\n")) + "\n"
		}
		b.WriteString(line)
	}

	if len(m.visible) == 0 {
		b.WriteString("  (empty)\n")
	}

	b.WriteString("------------------------------------------------------------\n")
	b.WriteString(fmt.Sprintf("Entries: %d/%d  Cursor: %d\n", len(m.visible), len(m.entries), min(m.cursor+1, len(m.visible))))
	if m.sizeFiltering {
		b.WriteString(fmt.Sprintf("Size Filter (%s): %s\n", m.sizeInputOp, renderInputWithCursor(m.sizeInput, m.sizeCursor)))
		b.WriteString("Enter:適用 Esc:キャンセル ←→ Home/End Backspace/Delete\n")
	} else if m.filtering {
		b.WriteString("Filter Input: " + renderInputWithCursor(m.filterInput, m.filterCursor) + "\n")
		b.WriteString("Enter:適用 Esc:キャンセル ←→ Home/End Backspace/Delete\n")
	} else if m.renaming {
		b.WriteString(fmt.Sprintf("Rename [%s]\n", m.renameOld))
		b.WriteString("New: " + renderInputWithCursor(m.renameInput, m.renameCursor) + "\n")
		b.WriteString("Enter:確認 Esc:キャンセル ←→ Home/End Backspace/Delete\n")
	} else {
		if m.pending != nil {
			b.WriteString(confirmStyle.Render(fmt.Sprintf("実行確認: %s ? (y/n)", m.pending.label)) + "\n")
		} else {
			b.WriteString("\n")
		}
		b.WriteString("Key: ↑↓←→ PgUp/PgDn Space d m c r f s S x o O R ? q\n")
	}
	if m.message != "" {
		b.WriteString("Msg: " + m.message + "\n")
	}
	if m.helpVisible {
		b.WriteString(helpView())
	}

	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func renderInputWithCursor(input string, cursor int) string {
	r := []rune(input)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(r) {
		cursor = len(r)
	}
	withCursor := make([]rune, 0, len(r)+1)
	withCursor = append(withCursor, r[:cursor]...)
	withCursor = append(withCursor, '|')
	withCursor = append(withCursor, r[cursor:]...)
	return string(withCursor)
}

func helpView() string {
	lines := []string{
		"",
		"Commands:",
		"  ?: このキーバインド一覧を表示/非表示",
		"  ↑ / ↓: カーソル移動",
		"  ←: 親ディレクトリへ移動",
		"  → / Enter: ディレクトリを開く / ファイル選択を切替",
		"  PgUp / PgDn: 1ページ移動",
		"  Space: 選択/解除を切り替え",
		"  d: 削除確認",
		"  m: カレントディレクトリへ移動確認",
		"  c: カレントディレクトリへコピー確認",
		"  r: リネーム入力開始",
		"  f: 名前フィルター入力",
		"  s: サイズ上限フィルター入力",
		"  S: サイズ下限フィルター入力",
		"  x: すべてのフィルターを解除",
		"  o: ソート項目を name -> size -> time で切替",
		"  O: ソート順を asc / desc で切替",
		"  R: 再読み込み",
		"  q: 終了確認",
		"  y / n: 確認ダイアログで実行 / キャンセル",
		"  Esc: 入力や確認をキャンセル",
	}
	return strings.Join(lines, "\n") + "\n"
}

func parseSizeInput(raw string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, fmt.Errorf("サイズを入力してください")
	}

	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return 0, fmt.Errorf("先頭に数値が必要です")
	}

	numPart := s[:i]
	unitPart := s[i:]
	val, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("数値形式が不正です")
	}
	if val < 0 {
		return 0, fmt.Errorf("負の値は指定できません")
	}

	var mul float64
	switch unitPart {
	case "", "B":
		mul = 1
	case "K", "KB":
		mul = 1024
	case "M", "MB":
		mul = 1024 * 1024
	case "G", "GB":
		mul = 1024 * 1024 * 1024
	case "T", "TB":
		mul = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("単位は B/KB/MB/GB/TB を使用してください")
	}

	return int64(val * mul), nil
}

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		os.Exit(1)
	}
}

func movePath(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !isCrossDevice(linkErr.Err) {
		return err
	}
	if err := copyPath(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst, info.Mode())
	}
	return copyFile(src, dst, info.Mode())
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(dst, mode.Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if err := copyPath(s, d); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func isCrossDevice(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && errno == syscall.EXDEV
}

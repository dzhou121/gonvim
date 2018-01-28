package editor

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/neovim/go-client/nvim"
	"github.com/therecipe/qt/core"
	"github.com/therecipe/qt/gui"
	"github.com/therecipe/qt/widgets"
)

// Window is
type Window struct {
	win        nvim.Window
	width      int
	height     int
	pos        [2]int
	tab        nvim.Tabpage
	hl         string
	bg         *RGBA
	statusline bool
	bufName    string
}

// Screen is the main editor area
type Screen struct {
	bg              *RGBA
	width           int
	height          int
	widget          *widgets.QWidget
	ws              *Workspace
	wins            map[nvim.Window]*Window
	cursor          [2]int
	lastCursor      [2]int
	content         [][]*Char
	scrollRegion    []int
	curtab          nvim.Tabpage
	cmdheight       int
	highlight       Highlight
	curWins         map[nvim.Window]*Window
	queueRedrawArea [4]int
	paintMutex      sync.Mutex
	redrawMutex     sync.Mutex
	drawSplit       bool
	tooltip         *widgets.QLabel
}

func newScreen() *Screen {
	widget := widgets.NewQWidget(nil, 0)
	widget.SetContentsMargins(0, 0, 0, 0)
	widget.SetAttribute(core.Qt__WA_OpaquePaintEvent, true)

	tooltip := widgets.NewQLabel(widget, 0)
	tooltip.SetVisible(false)
	tooltip.SetStyleSheet(`
		* {
			color: rgba(205, 211, 222, 1);
			background-color: rgba(24, 29, 34, 1);
			text-decoration: underline;
		}`)

	screen := &Screen{
		widget:       widget,
		cursor:       [2]int{0, 0},
		lastCursor:   [2]int{0, 0},
		scrollRegion: []int{0, 0, 0, 0},
		tooltip:      tooltip,
	}
	widget.ConnectPaintEvent(screen.paint)
	widget.ConnectMousePressEvent(screen.mouseEvent)
	widget.ConnectMouseReleaseEvent(screen.mouseEvent)
	widget.ConnectMouseMoveEvent(screen.mouseEvent)
	widget.ConnectResizeEvent(func(event *gui.QResizeEvent) {
		screen.updateSize()
	})
	widget.SetAttribute(core.Qt__WA_KeyCompression, false)

	return screen
}

func (s *Screen) updateSize() {
	w := s.ws
	s.width = s.widget.Width()
	cols := int(float64(s.width) / w.font.truewidth)
	rows := s.height / w.font.lineHeight

	if w.uiAttached {
		if cols != w.cols || rows != w.rows {
			w.nvim.TryResizeUI(cols, rows)
		}
	}
	w.cols = cols
	w.rows = rows
}

func (s *Screen) toolTipFont(font *Font) {
	s.tooltip.SetFont(font.fontNew)
	s.tooltip.SetContentsMargins(0, font.lineSpace/2, 0, font.lineSpace/2)
}

func (s *Screen) toolTip(text string) {
	s.tooltip.SetText(text)
	s.tooltip.AdjustSize()
	s.tooltip.Show()

	row := s.cursor[0]
	col := s.cursor[1]
	c := s.ws.cursor
	c.x = int(float64(col)*s.ws.font.truewidth) + s.tooltip.Width()
	c.y = row * s.ws.font.lineHeight
	c.move()
}

func (s *Screen) paint(vqp *gui.QPaintEvent) {
	s.paintMutex.Lock()
	defer s.paintMutex.Unlock()

	rect := vqp.M_rect()
	font := s.ws.font
	top := rect.Y()
	left := rect.X()
	width := rect.Width()
	height := rect.Height()
	right := left + width
	bottom := top + height
	row := int(float64(top) / float64(font.lineHeight))
	col := int(float64(left) / font.truewidth)
	rows := int(math.Ceil(float64(bottom)/float64(font.lineHeight))) - row
	cols := int(math.Ceil(float64(right)/font.truewidth)) - col

	p := gui.NewQPainter2(s.widget)
	if s.ws.background != nil {
		p.FillRect5(
			left,
			top,
			width,
			height,
			s.ws.background.QColor(),
		)
	}

	p.SetFont(font.fontNew)

	for y := row; y < row+rows; y++ {
		if y >= s.ws.rows {
			continue
		}
		s.fillHightlight(p, y, col, cols, [2]int{0, 0})
		s.drawText(p, y, col, cols, [2]int{0, 0})
	}

	s.drawBorder(p, row, col, rows, cols)
	p.DestroyQPainter()
	s.ws.markdown.updatePos()
}

func (s *Screen) mouseEvent(event *gui.QMouseEvent) {
	inp := s.convertMouse(event)
	if inp == "" {
		return
	}
	s.ws.nvim.Input(inp)
}

func (s *Screen) convertMouse(event *gui.QMouseEvent) string {
	font := s.ws.font
	x := int(float64(event.X()) / font.truewidth)
	y := int(float64(event.Y()) / float64(font.lineHeight))
	pos := []int{x, y}

	bt := event.Button()
	if event.Type() == core.QEvent__MouseMove {
		if event.Buttons()&core.Qt__LeftButton > 0 {
			bt = core.Qt__LeftButton
		} else if event.Buttons()&core.Qt__RightButton > 0 {
			bt = core.Qt__RightButton
		} else if event.Buttons()&core.Qt__MidButton > 0 {
			bt = core.Qt__MidButton
		} else {
			return ""
		}
	}

	mod := event.Modifiers()
	buttonName := ""
	switch bt {
	case core.Qt__LeftButton:
		buttonName += "Left"
	case core.Qt__RightButton:
		buttonName += "Right"
	case core.Qt__MidButton:
		buttonName += "Middle"
	case core.Qt__NoButton:
	default:
		return ""
	}

	evType := ""
	switch event.Type() {
	case core.QEvent__MouseButtonDblClick:
		evType += "Mouse"
	case core.QEvent__MouseButtonPress:
		evType += "Mouse"
	case core.QEvent__MouseButtonRelease:
		evType += "Release"
	case core.QEvent__MouseMove:
		evType += "Drag"
	default:
		return ""
	}

	return fmt.Sprintf("<%s%s%s><%d,%d>", editor.modPrefix(mod), buttonName, evType, pos[0], pos[1])
}

func (s *Screen) drawBorder(p *gui.QPainter, row, col, rows, cols int) {
	done := make(chan struct{})
	go func() {
		s.getWindows()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
	}
	for _, win := range s.curWins {
		if win.pos[0]+win.height < row && (win.pos[1]+win.width+1) < col {
			continue
		}
		if win.pos[0] > (row+rows) && (win.pos[1]+win.width) > (col+cols) {
			continue
		}

		win.drawBorder(p, s)
	}
}

func (s *Screen) getWindows() {
	wins := map[nvim.Window]*Window{}
	neovim := s.ws.nvim
	curtab, _ := neovim.CurrentTabpage()
	s.curtab = curtab
	nwins, _ := neovim.TabpageWindows(curtab)
	b := neovim.NewBatch()
	for _, nwin := range nwins {
		win := &Window{
			win: nwin,
		}
		b.WindowWidth(nwin, &win.width)
		b.WindowHeight(nwin, &win.height)
		b.WindowPosition(nwin, &win.pos)
		b.WindowTabpage(nwin, &win.tab)
		wins[nwin] = win
	}
	b.Option("cmdheight", &s.cmdheight)
	err := b.Execute()
	if err != nil {
		return
	}
	s.curWins = wins
	for _, win := range s.curWins {
		buf, _ := neovim.WindowBuffer(win.win)
		win.bufName, _ = neovim.BufferName(buf)

		if win.height+win.pos[0] < s.ws.rows-s.cmdheight {
			win.statusline = true
		} else {
			win.statusline = false
		}
		neovim.WindowOption(win.win, "winhl", &win.hl)
		if win.hl != "" {
			parts := strings.Split(win.hl, ",")
			for _, part := range parts {
				if strings.HasPrefix(part, "Normal:") {
					hl := part[7:]
					result := ""
					neovim.Eval(fmt.Sprintf("synIDattr(hlID('%s'), 'bg')", hl), &result)
					if result != "" {
						var r, g, b int
						format := "#%02x%02x%02x"
						n, err := fmt.Sscanf(result, format, &r, &g, &b)
						if err != nil {
							continue
						}
						if n != 3 {
							continue
						}
						win.bg = newRGBA(r, g, b, 1)
					}
				}
			}
		}
	}
}

func (s *Screen) updateBg(args []interface{}) {
	color := reflectToInt(args[0])
	if color == -1 {
		s.ws.background = newRGBA(0, 0, 0, 1)
	} else {
		bg := calcColor(reflectToInt(args[0]))
		s.ws.background = bg
	}
}

func (s *Screen) size() (int, int) {
	geo := s.widget.Geometry()
	return geo.Width(), geo.Height()
}

func (s *Screen) resize(args []interface{}) {
	s.cursor[0] = 0
	s.cursor[1] = 0
	s.content = make([][]*Char, s.ws.rows)
	for i := 0; i < s.ws.rows; i++ {
		s.content[i] = make([]*Char, s.ws.cols)
	}
	s.queueRedrawAll()
}

func (s *Screen) clear(args []interface{}) {
	s.cursor[0] = 0
	s.cursor[1] = 0
	s.content = make([][]*Char, s.ws.rows)
	for i := 0; i < s.ws.rows; i++ {
		s.content[i] = make([]*Char, s.ws.cols)
	}
	s.queueRedrawAll()
}

func (s *Screen) eolClear(args []interface{}) {
	row := s.cursor[0]
	col := s.cursor[1]
	if row >= s.ws.rows {
		return
	}
	line := s.content[row]
	numChars := 0
	for x := col; x < len(line); x++ {
		line[x] = nil
		numChars++
	}
	s.queueRedraw(col, row, numChars+1, 1)
}

func (s *Screen) cursorGoto(args []interface{}) {
	pos, _ := args[0].([]interface{})
	s.cursor[0] = reflectToInt(pos[0])
	s.cursor[1] = reflectToInt(pos[1])
}

func (s *Screen) put(args []interface{}) {
	numChars := 0
	x := s.cursor[1]
	y := s.cursor[0]
	row := s.cursor[0]
	col := s.cursor[1]
	if row >= s.ws.rows {
		return
	}
	line := s.content[row]
	oldFirstNormal := true
	char := line[x]
	if char != nil && !char.normalWidth {
		oldFirstNormal = false
	}
	var lastChar *Char
	oldNormalWidth := true
	for _, arg := range args {
		chars := arg.([]interface{})
		for _, c := range chars {
			if col >= len(line) {
				continue
			}
			char := line[col]
			if char != nil && !char.normalWidth {
				oldNormalWidth = false
			} else {
				oldNormalWidth = true
			}
			if char == nil {
				char = &Char{}
				line[col] = char
			}
			char.char = c.(string)
			char.normalWidth = s.isNormalWidth(char.char)
			lastChar = char
			char.highlight = s.highlight
			col++
			numChars++
		}
	}
	if lastChar != nil && !lastChar.normalWidth {
		numChars++
	}
	if !oldNormalWidth {
		numChars++
	}
	s.cursor[1] = col
	if x > 0 {
		char := line[x-1]
		if char != nil && char.char != "" && !char.normalWidth {
			x--
			numChars++
		} else {
			if !oldFirstNormal {
				x--
				numChars++
			}
		}
	}
	s.queueRedraw(x, y, numChars, 1)
}

func (s *Screen) highlightSet(args []interface{}) {
	for _, arg := range args {
		hl := arg.([]interface{})[0].(map[string]interface{})
		highlight := Highlight{}

		bold := hl["bold"]
		if bold != nil {
			highlight.bold = true
		} else {
			highlight.bold = false
		}

		italic := hl["italic"]
		if italic != nil {
			highlight.italic = true
		} else {
			highlight.italic = false
		}

		_, ok := hl["reverse"]
		if ok {
			highlight.foreground = s.highlight.background
			highlight.background = s.highlight.foreground
			s.highlight = highlight
			continue
		}

		fg, ok := hl["foreground"]
		if ok {
			rgba := calcColor(reflectToInt(fg))
			highlight.foreground = rgba
		} else {
			highlight.foreground = s.ws.foreground
		}

		bg, ok := hl["background"]
		if ok {
			rgba := calcColor(reflectToInt(bg))
			highlight.background = rgba
		} else {
			highlight.background = s.ws.background
		}
		s.highlight = highlight
	}
}

func (s *Screen) setScrollRegion(args []interface{}) {
	arg := args[0].([]interface{})
	top := reflectToInt(arg[0])
	bot := reflectToInt(arg[1])
	left := reflectToInt(arg[2])
	right := reflectToInt(arg[3])
	s.scrollRegion[0] = top
	s.scrollRegion[1] = bot
	s.scrollRegion[2] = left
	s.scrollRegion[3] = right
}

func (s *Screen) scroll(args []interface{}) {
	count := int(args[0].([]interface{})[0].(int64))
	top := s.scrollRegion[0]
	bot := s.scrollRegion[1]
	left := s.scrollRegion[2]
	right := s.scrollRegion[3]

	if top == 0 && bot == 0 && left == 0 && right == 0 {
		top = 0
		bot = s.ws.rows - 1
		left = 0
		right = s.ws.cols - 1
	}

	s.queueRedraw(left, top, (right - left + 1), (bot - top + 1))

	if count > 0 {
		for row := top; row <= bot-count; row++ {
			for col := left; col <= right; col++ {
				s.content[row][col] = s.content[row+count][col]
			}
		}
		for row := bot - count + 1; row <= bot; row++ {
			for col := left; col <= right; col++ {
				s.content[row][col] = nil
			}
		}
		s.queueRedraw(left, (bot - count + 1), (right - left), count)
		if top > 0 {
			s.queueRedraw(left, (top - count), (right - left), count)
		}
	} else {
		for row := bot; row >= top-count; row-- {
			for col := left; col <= right; col++ {
				s.content[row][col] = s.content[row+count][col]
			}
		}
		for row := top; row < top-count; row++ {
			for col := left; col <= right; col++ {
				s.content[row][col] = nil
			}
		}
		s.queueRedraw(left, top, (right - left), -count)
		if bot < s.ws.rows-1 {
			s.queueRedraw(left, bot+1, (right - left), -count)
		}
	}
}

func (s *Screen) update() {
	x := s.queueRedrawArea[0]
	y := s.queueRedrawArea[1]
	width := s.queueRedrawArea[2] - x
	height := s.queueRedrawArea[3] - y
	if width > 0 && height > 0 {
		// s.item.SetPixmap(s.pixmap)
		s.widget.Update2(
			int(float64(x)*s.ws.font.truewidth),
			y*s.ws.font.lineHeight,
			int(float64(width)*s.ws.font.truewidth),
			height*s.ws.font.lineHeight,
		)
	}
	s.queueRedrawArea[0] = s.ws.cols
	s.queueRedrawArea[1] = s.ws.rows
	s.queueRedrawArea[2] = 0
	s.queueRedrawArea[3] = 0
}

func (s *Screen) queueRedrawAll() {
	s.queueRedrawArea = [4]int{0, 0, s.ws.cols, s.ws.rows}
}

func (s *Screen) queueRedraw(x, y, width, height int) {
	if x < s.queueRedrawArea[0] {
		s.queueRedrawArea[0] = x
	}
	if y < s.queueRedrawArea[1] {
		s.queueRedrawArea[1] = y
	}
	if (x + width) > s.queueRedrawArea[2] {
		s.queueRedrawArea[2] = x + width
	}
	if (y + height) > s.queueRedrawArea[3] {
		s.queueRedrawArea[3] = y + height
	}
}

func (s *Screen) posWin(x, y int) *Window {
	for _, win := range s.curWins {
		if win.pos[0] <= y && win.pos[1] <= x && (win.pos[0]+win.height+1) >= y && (win.pos[1]+win.width >= x) {
			return win
		}
	}
	return nil
}

func (s *Screen) cursorWin() *Window {
	return s.posWin(s.cursor[1], s.cursor[0])
}

func (s *Screen) fillHightlight(p *gui.QPainter, y int, col int, cols int, pos [2]int) {
	rectF := core.NewQRectF()
	screen := s.ws.screen
	if y >= len(screen.content) {
		return
	}
	line := screen.content[y]
	start := -1
	end := -1
	var lastBg *RGBA
	var bg *RGBA
	var lastChar *Char
	for x := col; x < col+cols; x++ {
		if x >= len(line) {
			continue
		}
		char := line[x]
		if char != nil {
			bg = char.highlight.background
		} else {
			bg = nil
		}
		if lastChar != nil && !lastChar.normalWidth {
			bg = lastChar.highlight.background
		}
		if bg != nil {
			if lastBg == nil {
				start = x
				end = x
				lastBg = bg
			} else {
				if lastBg.equals(bg) {
					end = x
				} else {
					// last bg is different; draw the previous and start a new one
					rectF.SetRect(
						float64(start-pos[1])*s.ws.font.truewidth,
						float64((y-pos[0])*s.ws.font.lineHeight),
						float64(end-start+1)*s.ws.font.truewidth,
						float64(s.ws.font.lineHeight),
					)
					p.FillRect4(
						rectF,
						gui.NewQColor3(lastBg.R, lastBg.G, lastBg.B, int(lastBg.A*255)),
					)

					// start a new one
					start = x
					end = x
					lastBg = bg
				}
			}
		} else {
			if lastBg != nil {
				rectF.SetRect(
					float64(start-pos[1])*s.ws.font.truewidth,
					float64((y-pos[0])*s.ws.font.lineHeight),
					float64(end-start+1)*s.ws.font.truewidth,
					float64(s.ws.font.lineHeight),
				)
				p.FillRect4(
					rectF,
					gui.NewQColor3(lastBg.R, lastBg.G, lastBg.B, int(lastBg.A*255)),
				)

				// start a new one
				start = x
				end = x
				lastBg = nil
			}
		}
		lastChar = char
	}
	if lastBg != nil {
		rectF.SetRect(
			float64(start-pos[1])*s.ws.font.truewidth,
			float64((y-pos[0])*s.ws.font.lineHeight),
			float64(end-start+1)*s.ws.font.truewidth,
			float64(s.ws.font.lineHeight),
		)
		p.FillRect4(
			rectF,
			gui.NewQColor3(lastBg.R, lastBg.G, lastBg.B, int(lastBg.A*255)),
		)
	}
}

func (s *Screen) drawText(p *gui.QPainter, y int, col int, cols int, pos [2]int) {
	screen := s.ws.screen
	if y >= len(screen.content) {
		return
	}
	font := p.Font()
	font.SetBold(false)
	font.SetItalic(false)
	pointF := core.NewQPointF()
	line := screen.content[y]
	chars := map[Highlight][]int{}
	specialChars := []int{}
	if col > 0 {
		char := line[col-1]
		if char != nil && char.char != "" {
			if !char.normalWidth {
				col--
				cols++
			}
		}
	}
	if col+cols < s.ws.cols {
	}
	for x := col; x < col+cols; x++ {
		if x >= len(line) {
			continue
		}
		char := line[x]
		if char == nil {
			continue
		}
		if char.char == " " {
			continue
		}
		if char.char == "" {
			continue
		}
		if !char.normalWidth {
			specialChars = append(specialChars, x)
			continue
		}

		highlight := Highlight{}
		fg := char.highlight.foreground
		if fg == nil {
			fg = s.ws.foreground
		}
		highlight.foreground = fg
		highlight.italic = char.highlight.italic
		highlight.bold = char.highlight.bold

		colorSlice, ok := chars[highlight]
		if !ok {
			colorSlice = []int{}
		}
		colorSlice = append(colorSlice, x)
		chars[highlight] = colorSlice
	}

	for highlight, colorSlice := range chars {
		text := ""
		slice := colorSlice[:]
		for x := col; x < col+cols; x++ {
			if len(slice) == 0 {
				break
			}
			index := slice[0]
			if x < index {
				text += " "
				continue
			}
			if x == index {
				text += line[x].char
				slice = slice[1:]
			}
		}
		if text != "" {
			fg := highlight.foreground
			p.SetPen2(gui.NewQColor3(fg.R, fg.G, fg.B, int(fg.A*255)))
			pointF.SetX(float64(col-pos[1]) * s.ws.font.truewidth)
			pointF.SetY(float64((y-pos[0])*s.ws.font.lineHeight + s.ws.font.shift))
			font.SetBold(highlight.bold)
			font.SetItalic(highlight.italic)
			p.DrawText(pointF, text)
		}
	}

	for _, x := range specialChars {
		char := line[x]
		if char == nil || char.char == " " {
			continue
		}
		fg := char.highlight.foreground
		if fg == nil {
			fg = s.ws.foreground
		}
		p.SetPen2(gui.NewQColor3(fg.R, fg.G, fg.B, int(fg.A*255)))
		pointF.SetX(float64(x-pos[1]) * s.ws.font.truewidth)
		pointF.SetY(float64((y-pos[0])*s.ws.font.lineHeight + s.ws.font.shift))
		font.SetBold(char.highlight.bold)
		font.SetItalic(char.highlight.italic)
		p.DrawText(pointF, char.char)
	}
}

func (w *Window) drawBorder(p *gui.QPainter, s *Screen) {
	bg := s.ws.background
	if w.bg != nil {
		bg = w.bg
	}
	if bg == nil {
		return
	}
	height := w.height
	if w.statusline {
		height++
	}
	p.FillRect5(
		int(float64(w.pos[1]+w.width)*s.ws.font.truewidth),
		w.pos[0]*s.ws.font.lineHeight,
		int(s.ws.font.truewidth),
		height*s.ws.font.lineHeight,
		gui.NewQColor3(bg.R, bg.G, bg.B, 255),
	)
	p.FillRect5(
		int(float64(w.pos[1]+1+w.width)*s.ws.font.truewidth-1),
		w.pos[0]*s.ws.font.lineHeight,
		1,
		height*s.ws.font.lineHeight,
		gui.NewQColor3(0, 0, 0, 255),
	)

	gradient := gui.NewQLinearGradient3(
		(float64(w.width+w.pos[1])+1)*float64(s.ws.font.truewidth),
		0,
		(float64(w.width+w.pos[1])+1)*float64(s.ws.font.truewidth)-6,
		0,
	)
	gradient.SetColorAt(0, gui.NewQColor3(10, 10, 10, 125))
	gradient.SetColorAt(1, gui.NewQColor3(10, 10, 10, 0))
	brush := gui.NewQBrush10(gradient)
	p.FillRect2(
		int((float64(w.width+w.pos[1])+1)*s.ws.font.truewidth)-6,
		w.pos[0]*s.ws.font.lineHeight,
		6,
		height*s.ws.font.lineHeight,
		brush,
	)

	// p.FillRect5(
	// 	int(float64(w.pos[1])*editor.font.truewidth),
	// 	(w.pos[0]+w.height)*editor.font.lineHeight-1,
	// 	int(float64(w.width+1)*editor.font.truewidth),
	// 	1,
	// 	gui.NewQColor3(0, 0, 0, 255),
	// )

	if w.pos[0] > 0 {
		p.FillRect5(
			int(float64(w.pos[1])*s.ws.font.truewidth),
			w.pos[0]*s.ws.font.lineHeight-1,
			int(float64(w.width+1)*s.ws.font.truewidth),
			1,
			gui.NewQColor3(0, 0, 0, 255),
		)
	}
	gradient = gui.NewQLinearGradient3(
		float64(w.pos[1])*s.ws.font.truewidth,
		float64(w.pos[0]*s.ws.font.lineHeight),
		float64(w.pos[1])*s.ws.font.truewidth,
		float64(w.pos[0]*s.ws.font.lineHeight+5),
	)
	gradient.SetColorAt(0, gui.NewQColor3(10, 10, 10, 125))
	gradient.SetColorAt(1, gui.NewQColor3(10, 10, 10, 0))
	brush = gui.NewQBrush10(gradient)
	p.FillRect2(
		int(float64(w.pos[1])*s.ws.font.truewidth),
		w.pos[0]*s.ws.font.lineHeight,
		int(float64(w.width+1)*s.ws.font.truewidth),
		5,
		brush,
	)
}

func (s *Screen) isNormalWidth(char string) bool {
	if len(char) == 0 {
		return true
	}
	if char[0] <= 127 {
		return true
	}
	return s.ws.font.fontMetrics.Width(char) == s.ws.font.truewidth
}

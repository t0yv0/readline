package readline

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

type AutoCompleter interface {
	// Readline will pass the whole line and current offset to it
	// Completer need to pass all the candidates, and how long they shared the same characters in line
	// Example:
	//   [go, git, git-shell, grep]
	//   Do("g", 1) => ["o", "it", "it-shell", "rep"], 1
	//   Do("gi", 2) => ["t", "t-shell"], 2
	//   Do("git", 3) => ["", "-shell"], 3
	Do(line []rune, pos int) (newLine [][]rune, length int)
}

type AutoCompleterWithCandidates interface {
	// Readline will pass the whole line and current offset to it.
	//
	// Completer returns completion candidates specifying the updated line and how to display
	// the candidate option to the user.
	//
	// Example:
	//
	//     [go, git, git-shell]
	//     Do("run g", 4) => [{"run go", "go"}, {"run git", "git"}, {"run git-shell", "git-shell"}]
	//     Do("run gi", 5) => [{"run git-shell", "git-shell"}]
	//     Do("run git", 6) => [{"run git-shell", "git-shell"}]
	Complete(line []rune, pos int) []Candidate
}

type completerAdapter struct {
	AutoCompleter
}

func (c *completerAdapter) Complete(line []rune, pos int) (cs []Candidate) {
	lines, length := c.AutoCompleter.Do(line, pos)
	if len(lines) == 0 {
		return nil
	}
	for _, l := range lines {
		c := Candidate{
			NewLine: append(append(line[:pos], l...), line[pos:]...),
			Display: append(line[pos-length:pos], l...),
		}
		cs = append(cs, c)
	}
	return
}

type TabCompleter struct{}

func (t *TabCompleter) Do([]rune, int) ([][]rune, int) {
	return [][]rune{[]rune("\t")}, 0
}

type Candidate struct {
	NewLine []rune
	Display []rune
}

type opCompleter struct {
	w     io.Writer
	op    *Operation
	width int

	inCompleteMode  bool
	inSelectMode    bool
	candidate       []Candidate
	candidateSource []rune
	candidateChoise int
	candidateColNum int
}

func newOpCompleter(w io.Writer, op *Operation, width int) *opCompleter {
	return &opCompleter{
		w:     w,
		op:    op,
		width: width,
	}
}

func (o *opCompleter) doSelect() {
	if len(o.candidate) == 1 {
		o.writeCandidate(o.candidate[0])
		o.ExitCompleteMode(false)
		return
	}
	o.nextCandidate(1)
	o.CompleteRefresh()
}

func (o *opCompleter) nextCandidate(i int) {
	o.candidateChoise += i
	o.candidateChoise = o.candidateChoise % len(o.candidate)
	if o.candidateChoise < 0 {
		o.candidateChoise = len(o.candidate) + o.candidateChoise
	}
}

func (o *opCompleter) OnComplete() bool {
	if o.width == 0 {
		return false
	}
	if o.IsInCompleteSelectMode() {
		o.doSelect()
		return true
	}

	buf := o.op.buf
	rs := buf.Runes()

	if o.IsInCompleteMode() && o.candidateSource != nil && runes.Equal(rs, o.candidateSource) {
		o.EnterCompleteSelectMode()
		o.doSelect()
		return true
	}

	o.ExitCompleteSelectMode()
	o.candidateSource = rs

	var ac AutoCompleterWithCandidates
	if acc, ok := o.op.cfg.AutoComplete.(AutoCompleterWithCandidates); ok {
		ac = acc
	} else {
		ac = &completerAdapter{o.op.cfg.AutoComplete}
	}

	newLines := ac.Complete(rs, buf.idx)
	if len(newLines) == 0 {
		o.ExitCompleteMode(false)
		return true
	}

	// only Aggregate candidates in non-complete mode
	if !o.IsInCompleteMode() {
		if len(newLines) == 1 {
			o.writeCandidate(newLines[0])
			o.ExitCompleteMode(false)
			return true
		}

		if same, ok := o.aggregate(newLines); ok {
			o.writeCandidate(same)
			o.ExitCompleteMode(false)
			return true
		}
	}

	o.EnterCompleteMode(newLines)
	return true
}

func (o *opCompleter) aggregate(cs []Candidate) (Candidate, bool) {
	var newLines [][]rune
	newLines = append(newLines, o.candidateSource)
	for _, c := range cs {
		newLines = append(newLines, c.NewLine)
	}
	if same, size := runes.Aggregate(newLines); size > len(o.candidateSource) {
		return Candidate{NewLine: same}, true
	}
	return Candidate{}, false
}

func (o *opCompleter) IsInCompleteSelectMode() bool {
	return o.inSelectMode
}

func (o *opCompleter) IsInCompleteMode() bool {
	return o.inCompleteMode
}

func (o *opCompleter) HandleCompleteSelect(r rune) bool {
	next := true
	switch r {
	case CharEnter, CharCtrlJ:
		next = false
		o.writeCandidate(o.op.candidate[o.op.candidateChoise])
		o.ExitCompleteMode(false)
	case CharLineStart:
		num := o.candidateChoise % o.candidateColNum
		o.nextCandidate(-num)
	case CharLineEnd:
		num := o.candidateColNum - o.candidateChoise%o.candidateColNum - 1
		o.candidateChoise += num
		if o.candidateChoise >= len(o.candidate) {
			o.candidateChoise = len(o.candidate) - 1
		}
	case CharBackspace:
		o.ExitCompleteSelectMode()
		next = false
	case CharTab, CharForward:
		o.doSelect()
	case CharBell, CharInterrupt:
		o.ExitCompleteMode(true)
		next = false
	case CharNext:
		tmpChoise := o.candidateChoise + o.candidateColNum
		if tmpChoise >= o.getMatrixSize() {
			tmpChoise -= o.getMatrixSize()
		} else if tmpChoise >= len(o.candidate) {
			tmpChoise += o.candidateColNum
			tmpChoise -= o.getMatrixSize()
		}
		o.candidateChoise = tmpChoise
	case CharBackward:
		o.nextCandidate(-1)
	case CharPrev:
		tmpChoise := o.candidateChoise - o.candidateColNum
		if tmpChoise < 0 {
			tmpChoise += o.getMatrixSize()
			if tmpChoise >= len(o.candidate) {
				tmpChoise -= o.candidateColNum
			}
		}
		o.candidateChoise = tmpChoise
	default:
		next = false
		o.ExitCompleteSelectMode()
	}
	if next {
		o.CompleteRefresh()
		return true
	}
	return false
}

func (o *opCompleter) writeCandidate(c Candidate) {
	if runes.HasPrefix(c.NewLine, o.candidateSource) {
		o.op.buf.WriteRunes(c.NewLine[len(o.candidateSource):])
	} else {
		o.op.buf.Backspaces(len(o.candidateSource))
		o.op.buf.WriteRunes(c.NewLine)
	}
}

func (o *opCompleter) getMatrixSize() int {
	line := len(o.candidate) / o.candidateColNum
	if len(o.candidate)%o.candidateColNum != 0 {
		line++
	}
	return line * o.candidateColNum
}

func (o *opCompleter) OnWidthChange(newWidth int) {
	o.width = newWidth
}

func (o *opCompleter) CompleteRefresh() {
	if !o.inCompleteMode {
		return
	}
	lineCnt := o.op.buf.CursorLineCount()
	colWidth := 0
	for _, c := range o.candidate {
		w := runes.WidthAll(c.Display)
		if w > colWidth {
			colWidth = w
		}
	}

	colWidth += 1

	// -1 to avoid reach the end of line
	width := o.width - 1
	colNum := width / colWidth
	if colNum != 0 {
		colWidth += (width - (colWidth * colNum)) / colNum
	}

	o.candidateColNum = colNum
	buf := bufio.NewWriter(o.w)
	buf.Write(bytes.Repeat([]byte("\n"), lineCnt))

	colIdx := 0
	lines := 1
	buf.WriteString("\033[J")
	for idx, c := range o.candidate {
		inSelect := idx == o.candidateChoise && o.IsInCompleteSelectMode()
		if inSelect {
			buf.WriteString("\033[30;47m")
		}
		buf.WriteString(string(c.Display))
		buf.Write(bytes.Repeat([]byte(" "), colWidth-runes.WidthAll(c.Display)))

		if inSelect {
			buf.WriteString("\033[0m")
		}

		colIdx++
		if colIdx == colNum {
			buf.WriteString("\n")
			lines++
			colIdx = 0
		}
	}

	// move back
	fmt.Fprintf(buf, "\033[%dA\r", lineCnt-1+lines)
	fmt.Fprintf(buf, "\033[%dC", o.op.buf.idx+o.op.buf.PromptLen())
	buf.Flush()
}

func (o *opCompleter) aggCandidate(candidate [][]rune) int {
	offset := 0
	for i := 0; i < len(candidate[0]); i++ {
		for j := 0; j < len(candidate)-1; j++ {
			if i > len(candidate[j]) {
				goto aggregate
			}
			if candidate[j][i] != candidate[j+1][i] {
				goto aggregate
			}
		}
		offset = i
	}
aggregate:
	return offset
}

func (o *opCompleter) EnterCompleteSelectMode() {
	o.inSelectMode = true
	o.candidateChoise = -1
	o.CompleteRefresh()
}

func (o *opCompleter) EnterCompleteMode(candidates []Candidate) {
	o.inCompleteMode = true
	o.candidate = candidates
	o.CompleteRefresh()
}

func (o *opCompleter) ExitCompleteSelectMode() {
	o.inSelectMode = false
	o.candidate = nil
	o.candidateChoise = -1
	o.candidateSource = nil
}

func (o *opCompleter) ExitCompleteMode(revent bool) {
	o.inCompleteMode = false
	o.ExitCompleteSelectMode()
}

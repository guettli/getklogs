package getklogs

import (
	"bufio"
	"io"
)

const maxLogLineSize = 10 * 1024 * 1024

func newLineScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLogLineSize)
	return scanner
}

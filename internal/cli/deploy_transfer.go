package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type progressReadCloser struct {
	reader io.ReadCloser
	bytes  *atomic.Int64
}

func (r *progressReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.bytes.Add(int64(n))
	}
	return n, err
}

func (r *progressReadCloser) Close() error {
	return r.reader.Close()
}

type uploadProgress struct {
	label      string
	writer     io.Writer
	startedAt  time.Time
	tty        bool
	bytes      atomic.Int64
	totalBytes atomic.Int64
	done       chan struct{}
	stopped    chan struct{}
	mu         sync.Mutex
	lastPlain  time.Time
}

// plainProgressInterval spaces progress lines when stdout is not a terminal
// (CI logs, pipes) so logs show steady progress without a line per tick.
const plainProgressInterval = 10 * time.Second

func newUploadProgress(label string, totalBytes int64) *uploadProgress {
	progress := &uploadProgress{
		label:     label,
		writer:    os.Stdout,
		startedAt: time.Now(),
		tty:       canUseTUI() && strings.TrimSpace(os.Getenv("CI")) == "",
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	progress.totalBytes.Store(totalBytes)
	return progress
}

func (p *uploadProgress) Wrap(reader io.ReadCloser) io.ReadCloser {
	return &progressReadCloser{reader: reader, bytes: &p.bytes}
}

func (p *uploadProgress) SetCurrent(value int64) {
	p.bytes.Store(value)
}

func (p *uploadProgress) SetTotal(value int64) {
	p.totalBytes.Store(value)
}

// Note prints a message (for example a retry notice) on its own line without
// corrupting the live progress line.
func (p *uploadProgress) Note(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty {
		fmt.Fprintf(p.writer, "\r%s\r", strings.Repeat(" ", max(terminalWidth(80)-1, 1)))
	}
	fmt.Fprintln(p.writer, message)
}

func (p *uploadProgress) Start() {
	go func() {
		defer close(p.stopped)
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-p.done:
				p.render(true)
				return
			case <-ticker.C:
				p.render(false)
			}
		}
	}()
}

// Finish stops the progress display and waits for the final line, so output
// that follows never interleaves with it.
func (p *uploadProgress) Finish() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	select {
	case <-p.stopped:
	case <-time.After(time.Second):
	}
}

func (p *uploadProgress) render(final bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.bytes.Load()
	total := p.totalBytes.Load()
	if current > total {
		total = current
	}

	line := renderUploadProgressLine(p.label, current, total, time.Since(p.startedAt))
	if p.tty {
		fmt.Fprintf(p.writer, "\r%s", padProgressLine(line))
		if final {
			fmt.Fprint(p.writer, "\n")
		}
		return
	}

	now := time.Now()
	if final || (current > 0 && now.Sub(p.lastPlain) >= plainProgressInterval) {
		p.lastPlain = now
		fmt.Fprintln(p.writer, line)
	}
}

func renderUploadProgressLine(label string, current, total int64, elapsed time.Duration) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Uploading"
	}

	speed := "-"
	rate := 0.0
	seconds := elapsed.Seconds()
	if seconds > 0 {
		rate = float64(current) / seconds
		speed = formatBytes(int64(rate)) + "/s"
	}

	if total <= 0 {
		return fmt.Sprintf("%s  %s transferred  %s", label, formatBytes(current), speed)
	}

	barWidth := terminalWidth(80) - 60
	if barWidth < 12 {
		barWidth = 12
	}
	if barWidth > 32 {
		barWidth = 32
	}

	ratio := float64(current) / float64(total)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	bar := "[" + strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled) + "]"

	return fmt.Sprintf(
		"%s  %s  %5.1f%%  %s / %s  %s  %s",
		label,
		bar,
		ratio*100,
		formatBytes(current),
		formatBytes(total),
		speed,
		formatTransferETA(current, total, rate, elapsed),
	)
}

// formatTransferETA estimates the time left from the average rate so far. It
// stays "ETA --" until there is enough signal (2s and some bytes) to avoid
// wild early guesses.
func formatTransferETA(current, total int64, rate float64, elapsed time.Duration) string {
	if current >= total {
		return "done in " + formatShortDuration(elapsed)
	}
	if elapsed < 2*time.Second || rate <= 0 || current <= 0 {
		return "ETA --"
	}
	remaining := time.Duration(float64(total-current) / rate * float64(time.Second))
	return "ETA " + formatShortDuration(remaining)
}

func formatShortDuration(value time.Duration) string {
	value = value.Round(time.Second)
	if value < time.Minute {
		return fmt.Sprintf("%ds", int(value.Seconds()))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(value.Minutes()), int(value.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(value.Hours()), int(value.Minutes())%60)
}

func padProgressLine(line string) string {
	width := terminalWidth(80) - 1
	if width < 40 {
		return line
	}
	if len(line) >= width {
		return line[:width]
	}
	return line + strings.Repeat(" ", width-len(line))
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}

	div := int64(unit)
	suffix := "KB"
	for _, candidate := range []string{"MB", "GB", "TB"} {
		if value < div*unit {
			break
		}
		div *= unit
		suffix = candidate
	}
	return fmt.Sprintf("%.1f %s", float64(value)/float64(div), suffix)
}

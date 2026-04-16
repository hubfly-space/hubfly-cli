package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
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
	totalBytes int64
	writer     io.Writer
	startedAt  time.Time
	tty        bool
	bytes      atomic.Int64
	done       chan struct{}
}

func newUploadProgress(label string, totalBytes int64) *uploadProgress {
	return &uploadProgress{
		label:      label,
		totalBytes: totalBytes,
		writer:     os.Stdout,
		startedAt:  time.Now(),
		tty:        canUseTUI(),
		done:       make(chan struct{}),
	}
}

func (p *uploadProgress) Wrap(reader io.ReadCloser) io.ReadCloser {
	return &progressReadCloser{reader: reader, bytes: &p.bytes}
}

func (p *uploadProgress) Start() {
	go func() {
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

func (p *uploadProgress) Finish() {
	select {
	case <-p.done:
		return
	default:
		close(p.done)
	}
}

func (p *uploadProgress) render(final bool) {
	current := p.bytes.Load()
	total := p.totalBytes
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

	if final {
		fmt.Fprintln(p.writer, line)
	}
}

func renderUploadProgressLine(label string, current, total int64, elapsed time.Duration) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Uploading"
	}

	speed := "-"
	seconds := elapsed.Seconds()
	if seconds > 0 {
		speed = formatBytes(int64(float64(current)/seconds)) + "/s"
	}

	if total <= 0 {
		return fmt.Sprintf("%s  %s transferred  %s", label, formatBytes(current), speed)
	}

	barWidth := terminalWidth(80) - 48
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
		"%s  %s  %5.1f%%  %s / %s  %s",
		label,
		bar,
		ratio*100,
		formatBytes(current),
		formatBytes(total),
		speed,
	)
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

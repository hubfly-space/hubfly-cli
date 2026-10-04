package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseStatsOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantTarget  string
		wantProject string
		wantStream  bool
		wantErr     bool
	}{
		{
			name:        "empty args",
			args:        nil,
			wantTarget:  "",
			wantProject: "",
			wantStream:  false,
		},
		{
			name:        "positional target",
			args:        []string{"my-cell"},
			wantTarget:  "my-cell",
			wantProject: "",
			wantStream:  false,
		},
		{
			name:        "stream flag",
			args:        []string{"my-vm", "--stream"},
			wantTarget:  "my-vm",
			wantProject: "",
			wantStream:  true,
		},
		{
			name:        "short flags and project",
			args:        []string{"-f", "-p", "proj-9", "cell-1"},
			wantTarget:  "cell-1",
			wantProject: "proj-9",
			wantStream:  true,
		},
		{
			name:    "missing project argument",
			args:    []string{"--project"},
			wantErr: true,
		},
		{
			name:    "unexpected extra positional args",
			args:    []string{"target-one", "target-two"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseStatsOptions(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseStatsOptions(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if opts.target != tt.wantTarget {
					t.Errorf("target = %q, want %q", opts.target, tt.wantTarget)
				}
				if opts.projectFilter != tt.wantProject {
					t.Errorf("projectFilter = %q, want %q", opts.projectFilter, tt.wantProject)
				}
				if opts.stream != tt.wantStream {
					t.Errorf("stream = %v, want %v", opts.stream, tt.wantStream)
				}
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{2147483648, "2.0 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestStatsAPIOperations(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/c-123/metrics"):
			json.NewEncoder(w).Encode(map[string]any{
				"latest": map[string]any{
					"cpuPercent":       14.5,
					"memoryUsageBytes": 256 * 1024 * 1024,
					"memoryLimitBytes": 1024 * 1024 * 1024,
					"networkRxBytes":   1048576,
					"networkTxBytes":   2097152,
					"blockReadBytes":   4096,
					"blockWriteBytes":  8192,
				},
			})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/boxes/box-123/metrics"):
			json.NewEncoder(w).Encode(map[string]any{
				"status": "running",
				"metrics": map[string]any{
					"cpuUsagePercent": 28.2,
					"memoryUsageBytes": 512 * 1024 * 1024,
					"memoryTotalBytes": 2048 * 1024 * 1024,
					"diskUsageBytes":   4 * 1024 * 1024 * 1024,
					"diskTotalBytes":   20 * 1024 * 1024 * 1024,
					"networkRxBytes":   10240,
					"networkTxBytes":   20480,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	defer server.Close()

	apiHost = server.URL

	// 1. Fetch container metrics
	cMetrics, err := fetchContainerMetrics("test-token", "proj-1", "c-123")
	if err != nil {
		t.Fatalf("fetchContainerMetrics failed: %v", err)
	}
	if cMetrics.CPUPercent != 14.5 || cMetrics.MemoryUsageBytes != 256*1024*1024 {
		t.Fatalf("unexpected container metrics: %+v", cMetrics)
	}

	// 2. Fetch box metrics
	bMetrics, err := fetchBoxMetrics("test-token", "proj-1", "box-123")
	if err != nil {
		t.Fatalf("fetchBoxMetrics failed: %v", err)
	}
	if toFloat(bMetrics.Metrics["cpuUsagePercent"]) != 28.2 || toFloat(bMetrics.Metrics["memoryUsageBytes"]) != 512*1024*1024 {
		t.Fatalf("unexpected box metrics: %+v", bMetrics)
	}
}

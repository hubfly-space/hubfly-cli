package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

type statsOptions struct {
	target        string
	projectFilter string
	stream        bool
}

func parseStatsOptions(args []string) (statsOptions, error) {
	var opts statsOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return opts, errors.New("--project requires an argument")
			}
			opts.projectFilter = args[i+1]
			i++
		case "--stream", "-f", "--follow":
			opts.stream = true
		default:
			if !strings.HasPrefix(args[i], "-") && opts.target == "" {
				opts.target = args[i]
			} else {
				return opts, fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}
	return opts, nil
}

func runStatsCommand(args []string) error {
	opts, err := parseStatsOptions(args)
	if err != nil {
		return err
	}

	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if opts.stream {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			cancel()
		}()
	}

	for {
		err := renderStatsSnapshot(token, opts)
		if err != nil {
			return err
		}

		if !opts.stream {
			return nil
		}

		select {
		case <-ctx.Done():
			fmt.Println("\nStopped monitoring.")
			return nil
		case <-time.After(2 * time.Second):
			// Clear screen for next tick
			fmt.Print("\033[H\033[2J")
		}
	}
}

type statRow struct {
	targetName  string
	targetType  string
	projectName string
	status      string
	cpuStr      string
	memStr      string
	diskStr     string
	netStr      string
}

func renderStatsSnapshot(token string, opts statsOptions) error {
	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return err
	}

	var projects []project
	for _, p := range allProjects {
		if opts.projectFilter != "" && p.ID != opts.projectFilter && !strings.EqualFold(p.Name, opts.projectFilter) {
			continue
		}
		projects = append(projects, p)
	}

	var rows []statRow

	for _, p := range projects {
		// Collect containers
		if p.Type == "" || p.Type == "cell" {
			containers, cErr := fetchProjectContainers(token, p.ID)
			if cErr == nil {
				for _, c := range containers {
					if opts.target != "" && c.ID != opts.target && !strings.EqualFold(c.Name, opts.target) {
						continue
					}
					row := statRow{
						targetName:  c.Name,
						targetType:  "container",
						projectName: p.Name,
						status:      c.Status,
						cpuStr:      "-",
						memStr:      "-",
						diskStr:     "-",
						netStr:      "-",
					}

					metrics, mErr := fetchContainerMetrics(token, p.ID, c.ID)
					if mErr == nil && metrics != nil {
						row.cpuStr = fmt.Sprintf("%.1f%%", metrics.CPUPercent)
						if metrics.MemoryLimitBytes > 0 {
							row.memStr = fmt.Sprintf("%s / %s (%.1f%%)",
								formatBytes(metrics.MemoryUsageBytes),
								formatBytes(metrics.MemoryLimitBytes),
								metrics.MemoryUsagePercent,
							)
						} else if metrics.MemoryUsageBytes > 0 {
							row.memStr = formatBytes(metrics.MemoryUsageBytes)
						}

						if metrics.StorageTotalBytes > 0 {
							row.diskStr = fmt.Sprintf("%s / %s (%.1f%%)",
								formatBytes(metrics.StorageUsedBytes),
								formatBytes(metrics.StorageTotalBytes),
								metrics.StorageUsagePercent,
							)
						}

						if metrics.NetworkRxBytes > 0 || metrics.NetworkTxBytes > 0 {
							row.netStr = fmt.Sprintf("%s / %s",
								formatBytes(metrics.NetworkRxBytes),
								formatBytes(metrics.NetworkTxBytes),
							)
						}
					}

					rows = append(rows, row)
				}
			}
		}

		// Collect boxes
		if p.Type == "" || p.Type == "box" {
			boxes, bErr := fetchProjectBoxes(token, p.ID)
			if bErr == nil {
				for _, b := range boxes {
					if opts.target != "" && b.ID != opts.target && !strings.EqualFold(b.Name, opts.target) {
						continue
					}
					row := statRow{
						targetName:  b.Name,
						targetType:  "box",
						projectName: p.Name,
						status:      b.Status,
						cpuStr:      "-",
						memStr:      fmt.Sprintf("%d MiB", b.MemoryMiB),
						diskStr:     fmt.Sprintf("%d GiB", b.RootDiskGiB),
						netStr:      "-",
					}

					boxMetrics, mErr := fetchBoxMetrics(token, p.ID, b.ID)
					if mErr == nil && boxMetrics != nil && len(boxMetrics.Metrics) > 0 {
						if cpuVal, ok := boxMetrics.Metrics["cpu_percent"]; ok {
							row.cpuStr = fmt.Sprintf("%.1f%%", toFloat(cpuVal))
						} else if cpuVal, ok := boxMetrics.Metrics["cpuPercent"]; ok {
							row.cpuStr = fmt.Sprintf("%.1f%%", toFloat(cpuVal))
						}

						if memVal, ok := boxMetrics.Metrics["memory_rss_bytes"]; ok {
							row.memStr = fmt.Sprintf("%s / %d MiB", formatBytes(toInt64(memVal)), b.MemoryMiB)
						} else if memVal, ok := boxMetrics.Metrics["memoryUsageBytes"]; ok {
							row.memStr = fmt.Sprintf("%s / %d MiB", formatBytes(toInt64(memVal)), b.MemoryMiB)
						}

						rx := toInt64(boxMetrics.Metrics["net_rx_bytes"])
						tx := toInt64(boxMetrics.Metrics["net_tx_bytes"])
						if rx > 0 || tx > 0 {
							row.netStr = fmt.Sprintf("%s / %s", formatBytes(rx), formatBytes(tx))
						}
					}

					rows = append(rows, row)
				}
			}
		}
	}

	if len(rows) == 0 {
		if opts.target != "" {
			return fmt.Errorf("resource '%s' not found or has no active workloads", opts.target)
		}
		fmt.Println("No active containers or virtual machines found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TARGET\tTYPE\tSTATUS\tCPU\tMEM USAGE / LIMIT\tSTORAGE\tNET I/O (RX/TX)\tPROJECT")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.targetName,
			r.targetType,
			r.status,
			r.cpuStr,
			r.memStr,
			r.diskStr,
			r.netStr,
			r.projectName,
		)
	}
	return tw.Flush()
}


func toFloat(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func toInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

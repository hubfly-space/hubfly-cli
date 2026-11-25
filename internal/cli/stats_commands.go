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
		wait, err := renderStatsSnapshot(token, opts)
		if err != nil {
			retryWait, retry := containerRuntimeRetryWait(err)
			if !opts.stream || !retry {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s — retrying in %s...\n", err, retryWait.Round(time.Second))
			wait = retryWait
		}

		if !opts.stream {
			return nil
		}

		select {
		case <-ctx.Done():
			fmt.Println("\nStopped monitoring.")
			return nil
		case <-time.After(wait):
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

// renderStatsSnapshot prints one table and returns how long a --stream loop
// should wait before the next one: 2s, or a region's retryAfterSeconds when a
// container's region is unavailable. With a single container target, a
// runtime refusal (stopped, sleeping, region down, timeout) is returned as a
// friendly error instead of a row of dashes.
func renderStatsSnapshot(token string, opts statsOptions) (time.Duration, error) {
	wait := 2 * time.Second
	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return wait, err
	}
	var targetErr error

	var projects []project
	for _, p := range allProjects {
		if opts.projectFilter != "" && p.ID != opts.projectFilter && !strings.EqualFold(p.Name, opts.projectFilter) {
			continue
		}
		projects = append(projects, p)
	}

	type projectStats struct {
		rows      []statRow
		wait      time.Duration
		targetErr error
	}
	perProject, _ := fanOutProjects(projects, func(p project) (projectStats, error) {
		var rows []statRow
		wait := time.Duration(0)
		var targetErr error
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
					if mErr != nil {
						friendly := friendlyContainerRuntimeError(mErr, c.Name, "stats")
						var runtimeErr *containerRuntimeError
						if errors.As(friendly, &runtimeErr) {
							row.status = containerRuntimeStatusLabel(runtimeErr, row.status)
							if runtimeErr.retryAfter > wait {
								wait = runtimeErr.retryAfter
							}
							if opts.target != "" && targetErr == nil {
								targetErr = friendly
							}
						}
					}
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
		return projectStats{rows: rows, wait: wait, targetErr: targetErr}, nil
	})

	var rows []statRow
	for _, stats := range perProject {
		rows = append(rows, stats.rows...)
		if stats.wait > wait {
			wait = stats.wait
		}
		if targetErr == nil {
			targetErr = stats.targetErr
		}
	}

	if len(rows) == 0 {
		if opts.target != "" {
			return wait, fmt.Errorf("resource '%s' not found or has no active workloads", opts.target)
		}
		fmt.Println("No active containers or virtual machines found.")
		return wait, nil
	}
	if targetErr != nil && len(rows) == 1 {
		return wait, targetErr
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
	return wait, tw.Flush()
}

// containerRuntimeStatusLabel is the STATUS cell for a container whose stats
// the region refused.
func containerRuntimeStatusLabel(err *containerRuntimeError, fallback string) string {
	switch err.code {
	case containerNotRunning:
		if apiErr := (*apiError)(nil); errors.As(err.err, &apiErr) && apiErrorDetail(apiErr, "reason") == containerRuntimeMissing {
			return "no runtime"
		}
		return "stopped"
	case containerSleeping:
		return "sleeping"
	case containerRegionDown:
		return fmt.Sprintf("region unavailable (retry %s)", err.retryAfter.Round(time.Second))
	case containerRuntimeTimeout:
		return "timed out"
	}
	return fallback
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

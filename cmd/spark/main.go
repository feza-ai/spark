package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/feza-ai/spark/internal/bus"
	"github.com/feza-ai/spark/internal/cron"
	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/reconciler"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
	"github.com/feza-ai/spark/internal/watcher"
)

func main() {
	// Flags.
	natsURL := flag.String("nats", "nats://localhost:4222", "NATS server URL")
	nodeID := flag.String("node-id", defaultHostname(), "node identifier")
	manifestDir := flag.String("manifest-dir", "/etc/spark/manifests", "directory to watch for manifests")
	gpuMax := flag.Int("gpu-max", 1, "max concurrent GPU pods")
	heartbeatInterval := flag.Duration("heartbeat-interval", 10*time.Second, "heartbeat publish interval")
	reconcileInterval := flag.Duration("reconcile-interval", 5*time.Second, "reconciliation loop interval")
	systemReserveCPU := flag.Int("system-reserve-cpu", 2000, "CPU millicores reserved for system")
	systemReserveMem := flag.Int("system-reserve-memory", 4096, "MB of RAM reserved for system")
	stateDB := flag.String("state-db", "/var/lib/spark/state.db", "path to SQLite database file")
	podRetention := flag.Duration("pod-retention", 168*time.Hour, "retention for completed/failed pods")
	flag.Parse()

	slog.Info("spark starting",
		"node", *nodeID,
		"nats", *natsURL,
		"manifest-dir", *manifestDir,
	)

	// 1. Detect resources.
	gpuInfo, gpuErr := gpu.Detect()
	if gpuErr != nil && !errors.Is(gpuErr, gpu.ErrNoGPU) {
		slog.Error("gpu detection failed", "error", gpuErr)
		os.Exit(1)
	}
	if errors.Is(gpuErr, gpu.ErrNoGPU) {
		slog.Info("no GPU detected, running CPU-only")
	} else {
		slog.Info("GPU detected",
			"model", gpuInfo.Model,
			"memory_mb", gpuInfo.MemoryTotalMB,
			"count", gpuInfo.GPUCount,
		)
	}

	sysInfo, err := gpu.DetectSystem()
	if err != nil {
		slog.Error("system detection failed", "error", err)
		os.Exit(1)
	}
	slog.Info("system resources",
		"cpu_millis", sysInfo.CPUMillis,
		"memory_mb", sysInfo.MemoryTotalMB,
	)

	// 2. Create spark-net.
	if err := executor.EnsureNetwork(context.Background(), executor.DefaultNetwork); err != nil {
		slog.Error("failed to create network", "error", err)
		os.Exit(1)
	}

	// 3. Connect to NATS.
	b, err := bus.NewNATSBus(*natsURL)
	if err != nil {
		slog.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer b.Close()
	slog.Info("connected to NATS", "url", *natsURL)

	// 4. Create state store.
	store := state.NewPodStore()

	// 4a. Open SQLite and load persisted state.
	sqlStore, err := state.OpenSQLite(*stateDB)
	if err != nil {
		slog.Error("failed to open state database", "error", err)
		os.Exit(1)
	}
	defer sqlStore.Close()

	persisted, err := sqlStore.LoadAll()
	if err != nil {
		slog.Error("failed to load persisted state", "error", err)
		os.Exit(1)
	}
	if len(persisted) > 0 {
		store.LoadFrom(persisted)
		slog.Info("loaded persisted state", "pods", len(persisted))
	}

	store.OnDelete = func(name string) {
		if err := sqlStore.DeletePod(name); err != nil {
			slog.Error("failed to delete pod from SQLite", "pod", name, "error", err)
		}
	}

	// 5. Create resource tracker and scheduler.
	gpuMemMB := gpuInfo.MemoryTotalMB
	_ = gpuMax // reserved for future GPU slot limiting

	total := scheduler.Resources{
		CPUMillis:   sysInfo.CPUMillis,
		MemoryMB:    sysInfo.MemoryTotalMB,
		GPUMemoryMB: gpuMemMB,
	}
	reserve := scheduler.Resources{
		CPUMillis: *systemReserveCPU,
		MemoryMB:  *systemReserveMem,
	}
	tracker := scheduler.NewResourceTracker(total, reserve)
	sched := scheduler.NewScheduler(tracker)

	// 6. Create executor.
	exec := executor.NewPodmanExecutor(executor.DefaultNetwork)

	// 7. Register NATS handlers.
	priorityClasses := map[string]int{
		"system-critical": 0,
		"high":            100,
		"default":         1000,
		"low":             10000,
		"batch":           20000,
	}
	bus.RegisterApplyHandler(b, store, priorityClasses)
	bus.RegisterDeleteHandler(b, store, exec)
	bus.RegisterGetHandler(b, store)
	bus.RegisterListHandler(b, store)
	slog.Info("NATS handlers registered")

	// 8. Context for background goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 9. Create event publisher.
	eventPub := bus.NewEventPublisher(b)

	// 10. Create log streamer.
	logStreamer := bus.NewLogStreamer(b)

	// 11. Start reconciler.
	rec := reconciler.NewReconciler(store, sched, exec, *reconcileInterval)
	rec.SetOnStatusChange(func(podName, status, message string) {
		if err := eventPub.Publish(podName, status, message); err != nil {
			slog.Error("failed to publish lifecycle event", "pod", podName, "status", status, "error", err)
		}
		// Persist state change to SQLite.
		if podRec, ok := store.Get(podName); ok {
			if err := sqlStore.SavePod(&podRec); err != nil {
				slog.Error("failed to persist pod state", "pod", podName, "error", err)
			}
			if len(podRec.Events) > 0 {
				lastEvent := podRec.Events[len(podRec.Events)-1]
				if err := sqlStore.SaveEvent(podName, lastEvent); err != nil {
					slog.Error("failed to persist event", "pod", podName, "error", err)
				}
			}
		}
	})
	rec.OnPodRunning(func(podName string) {
		logStreamer.StartStream(podName, executor.StreamLogs)
	})
	rec.OnPodStopped(func(podName string) {
		logStreamer.StopStream(podName)
	})
	// 11a. Recover pods from podman.
	if err := rec.RecoverPods(ctx); err != nil {
		slog.Error("pod recovery failed", "error", err)
	}

	// 11b. Re-register recovered Running pods in scheduler.
	for _, pod := range store.List(state.StatusRunning) {
		sched.AddPod(scheduler.PodInfo{
			Name:      pod.Spec.Name,
			Priority:  pod.Spec.Priority,
			Resources: pod.Spec.TotalRequests(),
			StartTime: pod.StartedAt,
		})
	}

	go rec.Run(ctx)
	slog.Info("reconciler started", "interval", *reconcileInterval)

	// 11c. Start prune loop.
	go func() {
		pruneTicker := time.NewTicker(10 * time.Minute)
		defer pruneTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pruneTicker.C:
				cutoff := time.Now().Add(-*podRetention)
				pruned := store.Prune(*podRetention)
				if pruned > 0 {
					sqlPruned, err := sqlStore.PruneBefore(cutoff)
					if err != nil {
						slog.Error("failed to prune SQLite", "error", err)
					}
					slog.Info("pruned old pods", "in-memory", pruned, "sqlite", sqlPruned)
				}
			}
		}
	}()

	// 12. Start cron scheduler.
	cronSched := cron.NewCronScheduler(store)
	go cronSched.Run(ctx)
	slog.Info("cron scheduler started")

	// 13. Start heartbeat publisher.
	hb := bus.NewHeartbeatPublisher(b, *nodeID, tracker, store,
		gpuInfo.Model, gpuMemMB, sysInfo.CPUMillis, sysInfo.MemoryTotalMB)
	go hb.Run(ctx, *heartbeatInterval)
	slog.Info("heartbeat publisher started", "interval", *heartbeatInterval)

	// 14. Start directory watcher.
	go watcher.Watch(ctx, *manifestDir, func(event watcher.WatchEvent) {
		switch event.Type {
		case watcher.Added, watcher.Modified:
			result, err := manifest.Parse(event.Content, priorityClasses)
			if err != nil {
				slog.Error("failed to parse manifest", "path", event.Path, "error", err)
				return
			}
			for _, pod := range result.Pods {
				store.Apply(pod)
				slog.Info("applied pod from file", "pod", pod.Name, "path", event.Path)
				if podRec, ok := store.Get(pod.Name); ok {
					if err := sqlStore.SavePod(&podRec); err != nil {
						slog.Error("failed to persist applied pod", "pod", pod.Name, "error", err)
					}
				}
			}
			for _, cj := range result.CronJobs {
				if err := cronSched.Register(cj); err != nil {
					slog.Error("failed to register cronjob", "name", cj.Name, "error", err)
				} else {
					slog.Info("registered cronjob from file", "name", cj.Name, "path", event.Path)
				}
			}
		case watcher.Removed:
			slog.Info("manifest removed", "path", event.Path)
		}
	})
	slog.Info("directory watcher started", "dir", *manifestDir)

	slog.Info("spark ready")

	// 15. Block on OS signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	// Stop all log streams before cancelling context.
	logStreamer.StopAll()

	// Cancel context to stop reconciler, watcher, heartbeat, cron.
	cancel()

	// Give goroutines time to finish.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	<-shutdownCtx.Done()

	// Disconnect NATS last.
	b.Close()
	slog.Info("spark stopped")
}

func defaultHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "spark-node"
	}
	return h
}

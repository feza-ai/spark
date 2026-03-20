package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/feza-ai/spark/internal/api"
	"github.com/feza-ai/spark/internal/bus"
	"github.com/feza-ai/spark/internal/metrics"
	"github.com/feza-ai/spark/internal/cron"
	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/gpu"
	"github.com/feza-ai/spark/internal/lifecycle"
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
	httpAddr := flag.String("http-addr", ":8080", "HTTP listen address")
	shutdownTimeout := flag.Duration("shutdown-timeout", 30*time.Second, "max time to drain pods on shutdown")
	reconcileResourcesInterval := flag.Duration("reconcile-resources-interval", 60*time.Second, "resource reconciliation interval")
	logFormat := flag.String("log-format", "text", "log output format (text or json)")
	apiTokenFile := flag.String("api-token-file", "", "path to file containing API bearer token")
	flag.Parse()

	switch *logFormat {
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	case "text":
		// default, no change needed
	default:
		fmt.Fprintf(os.Stderr, "unknown log format: %s\n", *logFormat)
		os.Exit(1)
	}

	slog.Info("spark starting",
		"node", *nodeID,
		"nats", *natsURL,
		"manifest-dir", *manifestDir,
	)

	// 1. Detect resources.
	sysInfo, err := gpu.DetectSystem()
	if err != nil {
		slog.Error("system detection failed", "error", err)
		os.Exit(1)
	}
	slog.Info("system resources",
		"cpu_millis", sysInfo.CPUMillis,
		"memory_mb", sysInfo.MemoryTotalMB,
	)

	// Use system memory as fallback for unified-memory GPUs (e.g. NVIDIA GB10)
	// where nvidia-smi reports [N/A] for dedicated memory fields.
	fallbackMemory := func() (int, error) {
		return sysInfo.MemoryTotalMB, nil
	}
	gpuInfo, gpuErr := gpu.DetectWithFallback(fallbackMemory)
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
	slog.Info("connected to NATS", "url", *natsURL)

	// 4. Create state store.
	store := state.NewPodStore()

	// 4a. Open SQLite and load persisted state.
	sqlStore, err := state.OpenSQLite(*stateDB)
	if err != nil {
		slog.Error("failed to open state database", "error", err)
		os.Exit(1)
	}
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

	total := scheduler.Resources{
		CPUMillis:   sysInfo.CPUMillis,
		MemoryMB:    sysInfo.MemoryTotalMB,
		GPUMemoryMB: gpuMemMB,
	}
	reserve := scheduler.Resources{
		CPUMillis: *systemReserveCPU,
		MemoryMB:  *systemReserveMem,
	}
	tracker := scheduler.NewResourceTracker(total, reserve, gpuInfo.DeviceIDs, *gpuMax)
	sched := scheduler.NewScheduler(tracker)

	// 6. Create executor.
	exec := executor.NewPodmanExecutor(executor.DefaultNetwork)

	// 6a. Create cron scheduler (needed by NATS and HTTP handlers).
	cronSched := cron.NewCronScheduler(store)

	// 7. Register NATS handlers.
	priorityClasses := map[string]int{
		"system-critical": 0,
		"high":            100,
		"default":         1000,
		"low":             10000,
		"batch":           20000,
	}
	bus.RegisterApplyHandler(b, store, priorityClasses, cronSched)
	bus.RegisterDeleteHandler(b, store, exec, sched)
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

	// 12. Read API token.
	var apiToken string
	if *apiTokenFile != "" {
		tokenBytes, err := os.ReadFile(*apiTokenFile)
		if err != nil {
			slog.Error("failed to read API token file", "path", *apiTokenFile, "error", err)
			os.Exit(1)
		}
		apiToken = strings.TrimSpace(string(tokenBytes))
		slog.Info("API authentication enabled")
	}

	// 12a. Create metrics collector.
	metricsCollector := metrics.NewCollector(store, tracker, sched)

	// 13. Start HTTP API server.
	apiServer := api.NewServer(store, tracker, exec, priorityClasses, sqlStore, metricsCollector, cronSched, apiToken, sched, nil, nil)
	httpServer := &http.Server{Addr: *httpAddr, Handler: apiServer}
	go func() {
		slog.Info("HTTP server starting", "addr", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	// 13. Start resource reconciler.
	resReconciler := reconciler.NewResourceReconciler(store, exec, tracker, *reconcileResourcesInterval)
	go resReconciler.Run(ctx)
	slog.Info("resource reconciler started", "interval", *reconcileResourcesInterval)

	// 13a. Start prune loop.
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

	// 14. Start cron scheduler.
	go cronSched.Run(ctx)
	slog.Info("cron scheduler started")

	// 15. Start heartbeat publisher.
	hb := bus.NewHeartbeatPublisher(b, *nodeID, tracker, store,
		gpuInfo.Model, gpuInfo.GPUCount, gpuMemMB, sysInfo.CPUMillis, sysInfo.MemoryTotalMB)
	go hb.Run(ctx, *heartbeatInterval)
	slog.Info("heartbeat publisher started", "interval", *heartbeatInterval)

	// 16. Start directory watcher.
	// Track which cron jobs came from which manifest file.
	cronByPath := make(map[string][]string) // path -> cronjob names
	var cronByPathMu sync.Mutex

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
				if podRec, ok := store.Get(pod.Name); ok {
					podRec.SourcePath = event.Path
					store.SetSourcePath(pod.Name, event.Path)
					if err := sqlStore.SavePod(&podRec); err != nil {
						slog.Error("failed to persist applied pod", "pod", pod.Name, "error", err)
					}
				}
				slog.Info("applied pod from file", "pod", pod.Name, "path", event.Path)
			}
			cronByPathMu.Lock()
			var cronNames []string
			for _, cj := range result.CronJobs {
				if err := cronSched.Register(cj); err != nil {
					slog.Error("failed to register cronjob", "name", cj.Name, "error", err)
				} else {
					cronNames = append(cronNames, cj.Name)
					slog.Info("registered cronjob from file", "name", cj.Name, "path", event.Path)
				}
			}
			cronByPath[event.Path] = cronNames
			cronByPathMu.Unlock()
		case watcher.Removed:
			slog.Info("manifest removed, cleaning up", "path", event.Path)

			// Remove pods that came from this manifest file.
			for _, podRec := range store.ListBySourcePath(event.Path) {
				name := podRec.Spec.Name
				gracePeriod := podRec.Spec.TerminationGracePeriodSeconds
				if gracePeriod == 0 {
					gracePeriod = 10
				}
				if err := exec.StopPod(context.Background(), name, gracePeriod); err != nil {
					slog.Error("failed to stop pod on manifest removal", "pod", name, "error", err)
				}
				if err := exec.RemovePod(context.Background(), name); err != nil {
					slog.Error("failed to remove pod on manifest removal", "pod", name, "error", err)
				}
				store.Delete(name)
				sched.RemovePod(name)
				slog.Info("removed pod due to manifest deletion", "pod", name, "path", event.Path)
			}

			// Unregister cron jobs from this manifest file.
			cronByPathMu.Lock()
			for _, cronName := range cronByPath[event.Path] {
				cronSched.Unregister(cronName)
				slog.Info("unregistered cronjob due to manifest deletion", "name", cronName, "path", event.Path)
			}
			delete(cronByPath, event.Path)
			cronByPathMu.Unlock()
		}
	})
	slog.Info("directory watcher started", "dir", *manifestDir)

	slog.Info("spark ready")

	// Block on OS signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	// 1. Stop accepting new pods.
	store.SetReadOnly(true)
	slog.Info("store set to read-only")

	// 2. Shut down HTTP server gracefully.
	httpShutdownCtx, httpShutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer httpShutdownCancel()
	if err := httpServer.Shutdown(httpShutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	}
	slog.Info("HTTP server stopped")

	// 3. Stop log streams.
	logStreamer.StopAll()

	// 4. Cancel context to stop reconciler, watcher, heartbeat, cron, resource reconciler.
	cancel()

	// 5. Drain running pods.
	sc := lifecycle.NewShutdownCoordinator(store, exec, sched, *shutdownTimeout)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer drainCancel()
	if err := sc.Drain(drainCtx); err != nil {
		slog.Error("pod drain error", "error", err)
	}

	// 6. Close SQLite.
	sqlStore.Close()
	slog.Info("state database closed")

	// 7. Disconnect NATS.
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

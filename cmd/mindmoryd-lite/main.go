// Command mindmoryd-lite runs the single-process, JSONL-backed Mindmory
// control plane. No Postgres, no OpenSearch, no workers, no Docker — one
// binary, one data directory of canonical JSONL.
//
//	MINDMORY_DATA_DIR=var/data MINDMORY_HTTP_PORT=58080 mindmoryd-lite
//
// Env: MINDMORY_OWNER, MINDMORY_CURSOR_SIGNING_KEY,
// MINDMORY_MCP_CLIENT_TOKENS_JSON, MINDMORY_HTTP_PORT (default 58080),
// MINDMORY_ROOT_DIR (default .), and optional storage/embedding overrides
// documented in README.md and mindmory-config.example.sh.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mindmory.local/core/internal/lite"
	"mindmory.local/core/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_, _ = os.Stdout.WriteString("mindmoryd-lite " + version.Value + "\n")
		return
	}
	rebuildIndex := false
	embedAll := false
	verifyVectors := false
	vectorStatus := false
	rotateIntegrityKey := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--rebuild-index":
			rebuildIndex = true
		case "--embed":
			embedAll = true
			slog.Warn("--embed is deprecated; use --sync-vectors")
		case "--sync-vectors":
			embedAll = true
		case "--verify-vectors":
			verifyVectors = true
		case "--vector-status":
			vectorStatus = true
		case "--rotate-integrity-key":
			rotateIntegrityKey = true
		}
	}
	cfg, err := lite.LoadEnv(lite.LookupEnv)
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}

	store, err := lite.OpenConfigured(cfg.Storage, []byte(cfg.CursorKey))
	if err != nil {
		slog.Error("store open failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	embedder, err := lite.NewConfiguredEmbedder(cfg.Embedding)
	if err != nil {
		slog.Error("embedding config failed", "error", err)
		os.Exit(1)
	}
	if verifyVectors {
		if err := store.VerifyVectors(context.Background(), true); err != nil {
			slog.Error("vector verification failed", "error", err)
			os.Exit(1)
		}
		slog.Info("vector verification complete")
		os.Exit(0)
	}
	if vectorStatus {
		if err := json.NewEncoder(os.Stdout).Encode(store.VectorStatus()); err != nil {
			slog.Error("vector status failed", "error", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if rotateIntegrityKey {
		newKey := strings.TrimSpace(os.Getenv("MINDMORY_NEW_CURSOR_SIGNING_KEY"))
		record, err := store.RotateIntegrityKey([]byte(newKey))
		if err != nil {
			slog.Error("integrity key rotation failed", "error", err)
			os.Exit(1)
		}
		slog.Info("integrity key rotated", "previous_key_id", record.PreviousKeyID, "new_key_id", record.NewKeyID,
			"event_head_hash", record.EventHeadHash)
		os.Exit(0)
	}
	if rebuildIndex {
		if err := store.RebuildIndex(); err != nil {
			slog.Error("index rebuild failed", "error", err)
			os.Exit(1)
		}
		if store.Ops != nil {
			store.Ops.Record(lite.OpsEvent{Event: "INDEX_REBUILD", Outcome: "ok", Details: map[string]any{"mode": "manual"}})
		}
		slog.Info("index rebuilt from canonical JSONL")
		os.Exit(0)
	}
	if embedAll {
		if embedder == nil {
			slog.Error("vector sync requires an embedding provider; set MINDMORY_EMBED_PROVIDER")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		count, err := store.EmbedAll(ctx, embedder)
		if err != nil {
			slog.Error("embed backfill failed", "error", err)
			os.Exit(1)
		}
		slog.Info("embedding backfill complete", "rows_embedded", count)
		os.Exit(0)
	}

	// Import a legacy CSV export when the store is empty and export exists.
	if err := maybeImport(store, cfg.Storage.ExportDir); err != nil {
		slog.Error("import failed", "error", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Single-user local deployment: trust loopback for model-facing routes by
	// default. Administrative routes always retain token verification.
	trustLocal := strings.ToLower(envOr("MINDMORY_AUTH", "local")) != "token"
	address := envOr("MINDMORY_ADDRESS", "127.0.0.1:"+envOr("MINDMORY_HTTP_PORT", "58080"))
	if trustLocal && !loopbackAddress(address) {
		slog.Error("local auth requires an explicit loopback bind", "address", address)
		os.Exit(1)
	}
	server := lite.NewServer(store, cfg.Owner, cfg.CursorKey, envOr("MINDMORY_ADMIN_TOKEN", ""), cfg.MCPClients, log, trustLocal)
	server.Embedder = embedder
	server.SemanticSearch = &cfg.SemanticEnabled
	initialization := server.InitializeStatus(cfg.Storage, cfg.Embedding, cfg.SemanticEnabled)
	for _, incident := range initialization.Incidents {
		log.Warn("Mindmory startup action required", "code", incident.Code, "incident_id", incident.IncidentID,
			"message", incident.OperatorMessage, "commands", incident.CopyPasteCommands)
		if store.Ops != nil {
			store.Ops.Record(lite.OpsEvent{Event: incident.Code, Outcome: "action_required", Reason: incident.IncidentID,
				Details: map[string]any{"message": incident.OperatorMessage, "commands": incident.CopyPasteCommands}})
		}
	}
	if server.IntegrityError != nil {
		slog.Error("canonical integrity verification failed", "error", server.IntegrityError)
		os.Exit(1)
	}
	if trustLocal {
		localClientKey := strings.TrimSpace(envOr("MINDMORY_LOCAL_CLIENT_KEY", ""))
		if localClientKey == "" {
			slog.Error("local auth requires MINDMORY_LOCAL_CLIENT_KEY")
			os.Exit(1)
		}
		if _, ok := cfg.MCPClients[localClientKey]; !ok {
			slog.Error("local client key is not configured", "client_key", localClientKey)
			os.Exit(1)
		}
		server.LocalClientKey = localClientKey
		server.LearnerKey = localClientKey
	}

	if store.Ops != nil {
		store.Ops.Record(lite.OpsEvent{Event: "START", Outcome: "ok", Details: map[string]any{
			"version": version.Value, "edition": "lite", "data_dir": store.Dir(), "derived_dir": cfg.Storage.DerivedDir,
			"embedding_provider": cfg.Embedding.Provider, "embedding_model": cfg.Embedding.Model,
			"embedding_model_digest": cfg.Embedding.ModelDigest, "semantic": cfg.SemanticEnabled,
			"initialization_state": initialization.State,
		}})
	}
	httpServer := &http.Server{
		Addr:              address,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("mindmoryd-lite listening", "address", address, "data_dir", store.Dir())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server failed", "error", err)
			stop()
		}
	}()

	// Passive learner (Phase A): periodically propose durable-intent memories
	// from archived user turns. MINDMORY_LEARNER_INTERVAL controls the cadence
	// (default 5m; "0" disables). Current-turn cues APPLY immediately; older
	// cue-bearing messages STAGE for review (CURRENT_USER_EVIDENCE_REQUIRED).
	// Runs as the archive-owner MCP principal; no model is in the loop.
	if interval := parseLearnerInterval(envOr("MINDMORY_LEARNER_INTERVAL", "5m")); interval > 0 {
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					learnerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					summary, err := server.LearnerExtract(learnerCtx, server.LearnerPrincipal(), 50)
					cancel()
					if err != nil {
						slog.Error("learner extract failed", "error", err)
						continue
					}
					log.Info("learner extract complete",
						"scanned", summary.Scanned, "applied", summary.Applied,
						"staged", summary.Staged, "skipped", summary.Skipped, "failed", summary.Failed)
				}
			}
		}()
	}

	<-ctx.Done()
	if store.Ops != nil {
		store.Ops.Record(lite.OpsEvent{Event: "STOP", Outcome: "ok"})
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Info("mindmoryd-lite stopped")
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseLearnerInterval parses MINDMORY_LEARNER_INTERVAL: a Go duration, or
// "0" to disable the passive learner. Invalid or negative values disable it
// loudly rather than crashing the daemon.
func parseLearnerInterval(value string) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "0" {
		return 0
	}
	interval, err := time.ParseDuration(trimmed)
	if err != nil {
		slog.Error("invalid MINDMORY_LEARNER_INTERVAL", "value", trimmed, "error", err)
		return 0
	}
	if interval < 0 {
		return 0
	}
	return interval
}

// maybeImport loads var/export/*.csv into a fresh JSONL store. It is a
// one-time migration path; it no-ops once any canonical data exists.
func maybeImport(store *lite.Store, exportDir string) error {
	if store.HasData() {
		return nil
	}
	files, err := os.ReadDir(exportDir)
	if err != nil || len(files) == 0 {
		return nil
	}
	importer := lite.Importer{Store: store}
	if err := importer.ImportCSV(exportDir); err != nil {
		return err
	}
	_ = json.Valid
	if strings.TrimSpace(envOr("MINDMORY_IMPORT_VERBOSE", "")) != "" {
		slog.Info("legacy CSV export imported", "dir", exportDir)
	}
	return nil
}

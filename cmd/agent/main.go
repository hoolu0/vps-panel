package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hoolu0/vps-panel/internal/agent"
	"github.com/hoolu0/vps-panel/internal/proto"
)

func main() {
	var (
		configPath = flag.String("config", "/etc/vps-agent/config.json", "agent config file")
		panel      = flag.String("panel", "", "panel URL (overrides config)")
		token      = flag.String("token", "", "node token (overrides config)")
		dataDir    = flag.String("data", "", "data dir (overrides config)")
		dryRun     = flag.Bool("dryrun", false, "force dry-run for all tasks")
		version    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *version {
		fmt.Println("vps-agent", proto.Version)
		return
	}

	cfg := agent.Config{DataDir: "/var/lib/vps-agent"}
	if b, err := os.ReadFile(*configPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if *panel != "" {
		cfg.Panel = *panel
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *dryRun {
		cfg.ForceDryRun = true
	}
	if v := os.Getenv("VPS_AGENT_PANEL"); v != "" {
		cfg.Panel = v
	}
	if v := os.Getenv("VPS_AGENT_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("VPS_AGENT_DATA"); v != "" {
		cfg.DataDir = v
	}
	if os.Getenv("VPS_AGENT_DRYRUN") == "1" {
		cfg.ForceDryRun = true
	}
	if cfg.Panel == "" || cfg.Token == "" {
		log.Fatalf("panel URL and token are required (config %s, flags, or env)", *configPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("vps-agent %s starting (panel %s, data %s, dry-run %v)", proto.Version, cfg.Panel, cfg.DataDir, cfg.ForceDryRun)
	agent.New(cfg).Run(ctx)
	log.Printf("vps-agent stopped")
}

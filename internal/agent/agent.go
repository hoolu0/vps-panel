// Package agent implements the VPS-side agent: it connects to the panel
// over a WebSocket long connection, reports status, runs latency probes
// and executes tasks (initialization template, protocol deployment).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// Config is the agent configuration (written by the panel at install time,
// overridable by flags/env for manual installs and testing).
type Config struct {
	Panel       string `json:"panel"` // panel base URL or full ws URL
	Token       string `json:"token"`
	DataDir     string `json:"data_dir"`
	ForceDryRun bool   `json:"force_dry_run,omitempty"`
}

func (c *Config) wsURL() (string, error) {
	u, err := url.Parse(c.Panel)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported panel URL scheme %q", u.Scheme)
	}
	if !strings.HasSuffix(u.Path, "/agent/ws") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/agent/ws"
	}
	return u.String(), nil
}

// Agent is the running agent.
type Agent struct {
	cfg       Config
	collector *Collector
	sb        *SingBox

	mu   sync.Mutex // guards conn writes
	conn *websocket.Conn

	probeMu      sync.RWMutex
	probeTargets []proto.ProbeTarget
	probeEvery   time.Duration
	probeKick    chan struct{} // buffered(1): config change requests an immediate probe run

	taskCh chan proto.Task
}

// New creates an Agent.
func New(cfg Config) *Agent {
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/vps-agent"
	}
	sb := &SingBox{DataDir: cfg.DataDir}
	sb.Load()
	return &Agent{
		cfg:          cfg,
		collector:    &Collector{},
		sb:           sb,
		probeTargets: proto.DefaultProbeTargets(),
		probeEvery:   5 * time.Minute,
		probeKick:    make(chan struct{}, 1),
		taskCh:       make(chan proto.Task, 16),
	}
}

// Run connects to the panel and reconnects forever until ctx is done.
func (a *Agent) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.session(ctx); err != nil {
			log.Printf("panel connection lost: %v (retry in %s)", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

func (a *Agent) session(ctx context.Context) error {
	wsURL, err := a.cfg.wsURL()
	if err != nil {
		return err
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+a.cfg.Token)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, hdr)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.conn = nil
		a.mu.Unlock()
		conn.Close()
	}()
	log.Printf("connected to panel")

	hostname, _ := os.Hostname()
	hello := proto.Hello{
		AgentVersion: proto.Version,
		Hostname:     hostname,
		OS:           PrettyOS(),
		Kernel:       KernelRelease(),
		Arch:         runtime.GOARCH,
	}
	if err := a.send(proto.MsgHello, "", hello); err != nil {
		return err
	}
	if err := a.send(proto.MsgStatus, "", a.collector.Collect()); err != nil {
		return err
	}
	if err := a.sendDeployments(); err != nil {
		return err
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.statusLoop(sctx)
	go a.probeLoop(sctx)
	go a.taskWorker(sctx)
	go a.pingLoop(sctx)

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	})
	for {
		var env proto.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		switch env.Type {
		case proto.MsgTask:
			var t proto.Task
			if err := json.Unmarshal(env.Data, &t); err == nil {
				select {
				case a.taskCh <- t:
				default:
					a.sendTaskResult(t.ID, false, "agent task queue full", nil)
				}
			}
		case proto.MsgProbeConfig:
			var pc proto.ProbeConfig
			if err := json.Unmarshal(env.Data, &pc); err == nil {
				a.probeMu.Lock()
				changed := !probeTargetSetsEqual(a.probeTargets, pc.Targets)
				a.probeTargets = pc.Targets
				if pc.IntervalMin > 0 {
					a.probeEvery = time.Duration(pc.IntervalMin) * time.Minute
				}
				a.probeMu.Unlock()
				log.Printf("probe config updated: %d targets, every %s", len(a.currentTargets()), a.probeEvery)
				if changed {
					// Probe right away so the panel reflects the new
					// target set within seconds, not after a whole
					// probe interval.
					select {
					case a.probeKick <- struct{}{}:
					default:
					}
				}
			}
		}
	}
}

func (a *Agent) currentTargets() []proto.ProbeTarget {
	a.probeMu.RLock()
	defer a.probeMu.RUnlock()
	return append([]proto.ProbeTarget{}, a.probeTargets...)
}

// probeTargetSetsEqual reports whether two target lists point at the same
// IP+Family set (renames/reorders do not count as a change).
func probeTargetSetsEqual(a, b []proto.ProbeTarget) bool {
	setOf := func(ts []proto.ProbeTarget) map[string]bool {
		s := make(map[string]bool, len(ts))
		for _, t := range ts {
			s[fmt.Sprintf("%s|%d", t.IP, t.Family)] = true
		}
		return s
	}
	sa, sb := setOf(a), setOf(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}

func (a *Agent) send(typ, taskID string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("not connected")
	}
	_ = a.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return a.conn.WriteJSON(proto.Envelope{Type: typ, TaskID: taskID, Data: raw})
}

func (a *Agent) sendDeployments() error {
	return a.send(proto.MsgDeployments, "", a.sb.List())
}

func (a *Agent) sendTaskResult(taskID string, ok bool, errMsg string, result any) {
	res := proto.TaskResult{OK: ok, Error: errMsg}
	if result != nil {
		raw, _ := json.Marshal(result)
		res.Result = raw
	}
	_ = a.send(proto.MsgTaskResult, taskID, res)
}

func (a *Agent) statusLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = a.send(proto.MsgStatus, "", a.collector.Collect())
		}
	}
}

func (a *Agent) probeLoop(ctx context.Context) {
	// first probe shortly after connect
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	tick := func() {
		a.runProbes()
		a.probeMu.RLock()
		every := a.probeEvery
		a.probeMu.RUnlock()
		timer.Reset(every)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.probeKick:
			tick()
		case <-timer.C:
			tick()
		}
	}
}

func (a *Agent) runProbes() {
	rep := RunProbes(a.currentTargets())
	_ = a.send(proto.MsgProbe, "", rep)
}

func (a *Agent) pingLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			if a.conn != nil {
				_ = a.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}
			a.mu.Unlock()
		}
	}
}

// --- task execution -----------------------------------------------------------

func (a *Agent) taskWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-a.taskCh:
			a.execute(t)
		}
	}
}

func (a *Agent) execute(t proto.Task) {
	log.Printf("executing task %s (%s)", t.ID, t.Kind)
	logger := func(line string) {
		_ = a.send(proto.MsgTaskLog, t.ID, proto.TaskLog{Line: line})
	}
	r := &Runner{Log: logger, DryRun: a.cfg.ForceDryRun}

	switch t.Kind {
	case proto.TaskInit:
		var p proto.InitParams
		_ = json.Unmarshal(t.Params, &p)
		if p.DryRun {
			r.DryRun = true
		}
		result, err := RunInit(p, r)
		if err != nil {
			a.sendTaskResult(t.ID, false, err.Error(), result)
			return
		}
		a.sendTaskResult(t.ID, true, "", result)

	case proto.TaskFinalizeSSH:
		var p proto.FinalizeParams
		_ = json.Unmarshal(t.Params, &p)
		if err := RunFinalizeSSH(p, r); err != nil {
			a.sendTaskResult(t.ID, false, err.Error(), nil)
			return
		}
		a.sendTaskResult(t.ID, true, "", nil)

	case proto.TaskProbeNow:
		a.runProbes()
		a.sendTaskResult(t.ID, true, "", map[string]any{"targets": len(a.currentTargets())})

	case proto.TaskDeploy:
		var p proto.DeployParams
		if err := json.Unmarshal(t.Params, &p); err != nil {
			a.sendTaskResult(t.ID, false, "bad params: "+err.Error(), nil)
			return
		}
		dep, err := a.sb.Deploy(p, r)
		if err != nil {
			a.sendTaskResult(t.ID, false, err.Error(), nil)
			return
		}
		_ = a.sendDeployments()
		a.sendTaskResult(t.ID, true, "", dep)

	case proto.TaskUndeploy:
		var p proto.UndeployParams
		if err := json.Unmarshal(t.Params, &p); err != nil {
			a.sendTaskResult(t.ID, false, "bad params: "+err.Error(), nil)
			return
		}
		if err := a.sb.Undeploy(p.DeploymentID, r); err != nil {
			a.sendTaskResult(t.ID, false, err.Error(), nil)
			return
		}
		_ = a.sendDeployments()
		a.sendTaskResult(t.ID, true, "", nil)

	case proto.TaskRestartProto:
		if err := a.sb.RestartService(r); err != nil {
			a.sendTaskResult(t.ID, false, err.Error(), nil)
			return
		}
		a.sendTaskResult(t.ID, true, "", nil)

	case proto.TaskSync:
		hostname, _ := os.Hostname()
		_ = a.send(proto.MsgHello, "", proto.Hello{AgentVersion: proto.Version, Hostname: hostname, OS: PrettyOS(), Kernel: KernelRelease(), Arch: runtime.GOARCH})
		_ = a.send(proto.MsgStatus, "", a.collector.Collect())
		_ = a.sendDeployments()
		a.sendTaskResult(t.ID, true, "", nil)

	default:
		a.sendTaskResult(t.ID, false, "unknown task kind "+t.Kind, nil)
	}
}

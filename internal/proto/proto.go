// Package proto defines the wire protocol between panel and agent.
package proto

import (
	"encoding/json"
	"time"
)

// Envelope is the single WS message frame in both directions.
type Envelope struct {
	Type   string          `json:"type"`
	TaskID string          `json:"task_id,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Message types agent -> panel.
const (
	MsgHello       = "hello"
	MsgStatus      = "status"
	MsgDeployments = "deployments"
	MsgProbe       = "probe"
	MsgTaskLog     = "task_log"
	MsgTaskResult  = "task_result"
)

// Message types panel -> agent.
const (
	MsgTask        = "task"
	MsgProbeConfig = "probe_config"
)

// Task kinds.
const (
	TaskInit         = "init"          // run the VPS initialization template
	TaskFinalizeSSH  = "finalize_ssh"  // remove old SSH port after panel verified the new one
	TaskProbeNow     = "probe_now"     // run latency probes immediately
	TaskDeploy       = "deploy"        // install/update one protocol deployment
	TaskUndeploy     = "undeploy"      // remove one protocol deployment
	TaskRestartProto = "restart_proto" // restart the sing-box service
	TaskSync         = "sync"          // re-send hello/status/deployments
)

// Protocol module ids (pluggable deploy modules).
const (
	ModuleVLESSReality = "vless-reality"
	ModuleSS2022       = "ss2022"
)

// Hello is sent by the agent right after connecting.
type Hello struct {
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"` // pretty OS name, e.g. "Debian GNU/Linux 12"
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
}

// Status is one periodic status report from the agent.
type Status struct {
	At          time.Time `json:"at"`
	UptimeSec   int64     `json:"uptime_sec"`
	Load1       float64   `json:"load1"`
	Load5       float64   `json:"load5"`
	Load15      float64   `json:"load15"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemUsed     uint64    `json:"mem_used"`
	MemTotal    uint64    `json:"mem_total"`
	SwapUsed    uint64    `json:"swap_used"`
	SwapTotal   uint64    `json:"swap_total"`
	DiskUsed    uint64    `json:"disk_used"`
	DiskTotal   uint64    `json:"disk_total"`
	NetInSpeed  uint64    `json:"net_in_speed"`  // bytes/s
	NetOutSpeed uint64    `json:"net_out_speed"` // bytes/s
	NetInTotal  uint64    `json:"net_in_total"`
	NetOutTotal uint64    `json:"net_out_total"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Kernel      string    `json:"kernel"`
	Arch        string    `json:"arch"`
}

// Task is a unit of work pushed from panel to agent.
type Task struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params,omitempty"`
}

// TaskLog is one streamed log line for a running task.
type TaskLog struct {
	Line string `json:"line"`
}

// TaskResult terminates a task.
type TaskResult struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// InitParams are the parameters of the initialization template.
type InitParams struct {
	Timezone   string `json:"timezone"`     // e.g. "Asia/Shanghai"
	NewSSHPort int    `json:"new_ssh_port"` // 0 = keep current port
	DryRun     bool   `json:"dry_run,omitempty"`
	// Steps selects which template steps run (see the InitStep* ids).
	// Empty/nil means all steps, keeping older callers (e.g. the
	// automatic post-onboarding init) on the full template.
	Steps []string `json:"steps,omitempty"`
}

// Initialization template step ids (InitParams.Steps).
const (
	InitStepUpdate         = "update"          // system package update/upgrade
	InitStepTools          = "tools"           // common tools installation
	InitStepTimezone       = "timezone"        // set timezone
	InitStepTimesync       = "timesync"        // enable time synchronization
	InitStepUFW            = "ufw"             // ufw firewall
	InitStepFail2ban       = "fail2ban"        // fail2ban for sshd
	InitStepSSHPort        = "ssh_port"        // change SSH port
	InitStepBBR            = "bbr"             // enable BBR + fq
	InitStepSwap           = "swap"            // add swap on small-memory hosts
	InitStepAgentAutostart = "agent_autostart" // agent starts on boot
)

// DeployParams are the parameters of a protocol deployment task.
type DeployParams struct {
	DeploymentID string `json:"deployment_id"`
	Module       string `json:"module"`
	Port         int    `json:"port"` // 0 = agent picks a random free port
	// VLESS+Reality options (optional; defaults applied by the agent).
	Dest       string `json:"dest,omitempty"`        // e.g. "www.microsoft.com:443"
	ServerName string `json:"server_name,omitempty"` // e.g. "www.microsoft.com"
}

// Version of the panel/agent build.
const Version = "0.3.0"

// FinalizeParams are the parameters of the finalize_ssh task, sent by the
// panel after it has verified the new SSH port is reachable.
type FinalizeParams struct {
	NewSSHPort int `json:"new_ssh_port"`
	OldSSHPort int `json:"old_ssh_port"`
}

// UndeployParams identify the deployment to remove.
type UndeployParams struct {
	DeploymentID string `json:"deployment_id"`
}

// Deployment is one installed protocol instance as reported by the agent.
// The panel composes share links from these fields.
type Deployment struct {
	ID        string    `json:"id"`
	Module    string    `json:"module"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	// VLESS+Reality
	UUID       string `json:"uuid,omitempty"`
	PublicKey  string `json:"public_key,omitempty"`
	ShortID    string `json:"short_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	Dest       string `json:"dest,omitempty"`
	// Shadowsocks 2022
	Method   string `json:"method,omitempty"`
	Password string `json:"password,omitempty"`
	// Panel-side public endpoint override (e.g. NAT port mapping where the
	// external host/port differs from the listen endpoint). Used only when
	// composing share links on the panel; never sent to the agent, which
	// keeps listening on Port.
	PublicHost string `json:"public_host,omitempty"`
	PublicPort int    `json:"public_port,omitempty"`
}

// ProbeTarget is one latency probe destination.
type ProbeTarget struct {
	Name    string `json:"name"`    // display name, e.g. "北京电信"
	Carrier string `json:"carrier"` // CT / CU / CM / OTHER
	IP      string `json:"ip"`
	Family  int    `json:"family"` // 4 or 6
}

// ProbeConfig is pushed from panel to agent.
type ProbeConfig struct {
	Targets     []ProbeTarget `json:"targets"`
	IntervalMin int           `json:"interval_min"`
}

// ProbeResult is the outcome of probing one target.
type ProbeResult struct {
	Name    string  `json:"name"`
	Carrier string  `json:"carrier"`
	IP      string  `json:"ip"`
	Family  int     `json:"family"`
	AvgMs   float64 `json:"avg_ms"` // -1 when unreachable
	LossPct float64 `json:"loss_pct"`
	Error   string  `json:"error,omitempty"`
}

// ProbeReport bundles probe results.
type ProbeReport struct {
	At      time.Time     `json:"at"`
	Results []ProbeResult `json:"results"`
}

// DefaultProbeTargets are the shipped three-network latency targets.
// Users can add/edit/remove targets (incl. IPv6) in the panel settings.
func DefaultProbeTargets() []ProbeTarget {
	return []ProbeTarget{
		{Name: "北京电信", Carrier: "CT", IP: "219.141.136.10", Family: 4},
		{Name: "上海电信", Carrier: "CT", IP: "202.96.209.5", Family: 4},
		{Name: "北京联通", Carrier: "CU", IP: "202.106.195.68", Family: 4},
		{Name: "上海联通", Carrier: "CU", IP: "210.22.84.3", Family: 4},
		{Name: "广州移动", Carrier: "CM", IP: "120.196.165.7", Family: 4},
		{Name: "北京移动", Carrier: "CM", IP: "221.179.155.161", Family: 4},
		{Name: "阿里公共DNSv6", Carrier: "OTHER", IP: "2400:3200::1", Family: 6},
		{Name: "腾讯公共DNSv6", Carrier: "OTHER", IP: "2402:4e00::", Family: 6},
	}
}

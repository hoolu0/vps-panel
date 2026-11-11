package panel

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Agent binaries cross-compiled by scripts/build.sh and embedded into the
// panel. Placeholder files are committed so the package always builds;
// Onboard checks the size and refuses placeholders.
//
//go:embed agentbin/vps-agent-linux-amd64
var agentBinAMD64 []byte

//go:embed agentbin/vps-agent-linux-arm64
var agentBinARM64 []byte

func agentBinary(arch string) ([]byte, error) {
	var b []byte
	switch arch {
	case "amd64":
		b = agentBinAMD64
	case "arm64":
		b = agentBinARM64
	default:
		return nil, fmt.Errorf("不支持的架构 %q", arch)
	}
	if len(b) < 1024 {
		return nil, fmt.Errorf("面板未内嵌 %s 版 agent 二进制（请先运行 scripts/build.sh 构建）", arch)
	}
	return b, nil
}

// OnboardRequest is the payload for adding a VPS.
type OnboardRequest struct {
	Name       string `json:"name"`
	IP         string `json:"ip"`
	SSHPort    int    `json:"ssh_port"`
	Username   string `json:"username"`  // default "root"
	AuthType   string `json:"auth_type"` // "password" or "key"
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	KeyPass    string `json:"key_passphrase,omitempty"`
	Timezone   string `json:"timezone"`
	NewSSHPort int    `json:"new_ssh_port"`
	RunInit    bool   `json:"run_init"`
}

// onboardSSH connects by SSH, installs the agent and starts it. The
// credentials are used for this call only and never stored.
func onboardSSH(panelBase string, node *Node, req OnboardRequest) ([]string, error) {
	logs := []string{}
	step := func(s string) { logs = append(logs, s) }

	var authMethods []ssh.AuthMethod
	switch req.AuthType {
	case "key":
		var signer ssh.Signer
		var err error
		if req.KeyPass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(req.PrivateKey), []byte(req.KeyPass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(req.PrivateKey))
		}
		if err != nil {
			return logs, fmt.Errorf("解析私钥失败： %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	default:
		authMethods = append(authMethods, ssh.Password(req.Password))
	}
	user := req.Username
	if user == "" {
		user = "root"
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", req.IP, req.SSHPort)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return logs, fmt.Errorf("SSH 连接 %s 失败： %w", addr, err)
	}
	defer client.Close()
	step("SSH 连接成功：" + addr)

	run := func(cmd string, stdin []byte) (string, error) {
		sess, err := client.NewSession()
		if err != nil {
			return "", err
		}
		defer sess.Close()
		if stdin != nil {
			sess.Stdin = bytes.NewReader(stdin)
		}
		out, err := sess.CombinedOutput(cmd)
		return strings.TrimSpace(string(out)), err
	}

	archOut, err := run("uname -m", nil)
	if err != nil {
		return logs, fmt.Errorf("探测远端架构失败： %w", err)
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[archOut]
	if arch == "" {
		return logs, fmt.Errorf("不支持的远端架构 %q", archOut)
	}
	step("远端架构：" + archOut + " → agent " + arch)

	bin, err := agentBinary(arch)
	if err != nil {
		return logs, err
	}
	if _, err := run("mkdir -p /etc/vps-agent /var/lib/vps-agent", nil); err != nil {
		return logs, fmt.Errorf("创建目录失败： %w", err)
	}
	step("上传 agent 二进制（" + fmt.Sprint(len(bin)) + " 字节）…")
	if _, err := run("cat > /tmp/vps-agent.bin", bin); err != nil {
		return logs, fmt.Errorf("上传 agent 失败： %w", err)
	}
	if _, err := run("install -m 755 /tmp/vps-agent.bin /usr/local/bin/vps-agent && rm -f /tmp/vps-agent.bin", nil); err != nil {
		return logs, fmt.Errorf("安装 agent 失败： %w", err)
	}

	agentCfg := map[string]any{
		"panel":    panelBase,
		"token":    node.Token,
		"data_dir": "/var/lib/vps-agent",
	}
	cfgJSON, _ := json.MarshalIndent(agentCfg, "", "  ")
	if _, err := run("cat > /etc/vps-agent/config.json", cfgJSON); err != nil {
		return logs, fmt.Errorf("写入 agent 配置失败： %w", err)
	}
	if _, err := run("chmod 600 /etc/vps-agent/config.json", nil); err != nil {
		return logs, fmt.Errorf("设置配置权限失败： %w", err)
	}
	step("agent 配置已写入")

	kind := detectRemoteInit(run)
	step("远端 init 系统：" + kind)
	switch kind {
	case "openrc":
		if err := installAgentOpenRC(run, step); err != nil {
			return logs, err
		}
	case "none":
		if err := installAgentNohup(run, step); err != nil {
			return logs, err
		}
	default:
		if err := installAgentSystemd(run, step); err != nil {
			return logs, err
		}
	}
	step("接入完成，等待 agent 上线…")
	return logs, nil
}

// detectRemoteInit probes the remote host's init system over SSH. The
// panel process env VPS_FORCE_INIT (systemd|openrc|none) overrides the
// probe — used for testing; the agent has the same override on its side.
func detectRemoteInit(run func(string, []byte) (string, error)) string {
	switch os.Getenv("VPS_FORCE_INIT") {
	case "systemd", "openrc", "none":
		return os.Getenv("VPS_FORCE_INIT")
	}
	probe := `if [ -d /run/systemd/system ] || [ "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd" ]; then echo systemd; elif command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ] || { [ "$(ps -p 1 -o comm= 2>/dev/null)" = "init" ] && [ -d /etc/init.d ]; }; then echo openrc; else echo none; fi`
	out, err := run(probe, nil)
	if err != nil {
		return "systemd" // fall back to the pre-0.2.1 behavior
	}
	switch strings.TrimSpace(out) {
	case "openrc", "none":
		return strings.TrimSpace(out)
	default:
		return "systemd"
	}
}

func installAgentSystemd(run func(string, []byte) (string, error), step func(string)) error {
	unit := `[Unit]
Description=VPS Panel Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/vps-agent -config /etc/vps-agent/config.json
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
`
	if _, err := run("mkdir -p /etc/systemd/system", nil); err != nil {
		return fmt.Errorf("创建 systemd 目录失败： %w", err)
	}
	if _, err := run("cat > /etc/systemd/system/vps-agent.service", []byte(unit)); err != nil {
		return fmt.Errorf("写入 systemd 服务失败： %w", err)
	}
	out, err := run("systemctl daemon-reload && systemctl enable --now vps-agent && systemctl is-active vps-agent", nil)
	if err != nil {
		return fmt.Errorf("启动 agent 服务失败： %w（输出： %s）", err, out)
	}
	step("agent 服务状态：" + out)
	return nil
}

func installAgentOpenRC(run func(string, []byte) (string, error), step func(string)) error {
	script := `#!/sbin/openrc-run
name="vps-agent"
description="VPS Panel Agent"
command="/usr/local/bin/vps-agent"
command_args="-config /etc/vps-agent/config.json"
command_background=true
pidfile="/run/vps-agent.pid"
output_log="/var/log/vps-agent.log"
error_log="/var/log/vps-agent.log"

depend() {
	need net
}
`
	if _, err := run("mkdir -p /etc/init.d", nil); err != nil {
		return fmt.Errorf("创建 init.d 目录失败： %w", err)
	}
	if _, err := run("cat > /etc/init.d/vps-agent", []byte(script)); err != nil {
		return fmt.Errorf("写入 OpenRC 服务脚本失败： %w", err)
	}
	if _, err := run("chmod +x /etc/init.d/vps-agent", nil); err != nil {
		return fmt.Errorf("设置服务脚本权限失败： %w", err)
	}
	if out, err := run("rc-update add vps-agent default", nil); err != nil {
		step("rc-update add 输出：" + out + "（已忽略，继续启动）")
	}
	_, _ = run("rc-service vps-agent stop", nil) // tolerate: may not be running yet
	out, err := run("rc-service vps-agent start", nil)
	if err != nil {
		// start exit status is not always reliable; a live process counts.
		if pid, perr := run("pidof vps-agent", nil); perr != nil || pid == "" {
			return fmt.Errorf("启动 agent 服务失败（OpenRC）： %w（输出： %s）", err, out)
		}
	}
	step("agent 已通过 OpenRC 启动")
	return nil
}

func installAgentNohup(run func(string, []byte) (string, error), step func(string)) error {
	out, err := run("nohup /usr/local/bin/vps-agent -config /etc/vps-agent/config.json </dev/null >/var/log/vps-agent.log 2>&1 & sleep 1; pidof vps-agent", nil)
	if err != nil || out == "" {
		return fmt.Errorf("nohup 启动 agent 失败（输出： %s）", out)
	}
	step("无 systemd/OpenRC，已用 nohup 启动 agent（PID " + out + "），重启不自动恢复")
	return nil
}

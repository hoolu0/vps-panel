package panel

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
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
	if _, err := run("cat > /etc/systemd/system/vps-agent.service", []byte(unit)); err != nil {
		return logs, fmt.Errorf("写入 systemd 服务失败： %w", err)
	}
	out, err := run("systemctl daemon-reload && systemctl enable --now vps-agent && systemctl is-active vps-agent", nil)
	if err != nil {
		return logs, fmt.Errorf("启动 agent 服务失败： %w（输出： %s）", err, out)
	}
	step("agent 服务状态：" + out)
	step("接入完成，等待 agent 上线…")
	return logs, nil
}

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// --- helpers ----------------------------------------------------------------

func pkgManager(r *Runner) string {
	for _, c := range []string{"apt-get", "dnf", "yum"} {
		if _, err := r.Capture("which", c); err == nil {
			switch c {
			case "apt-get":
				return "apt"
			default:
				return c
			}
		}
		if _, err := os.Stat("/usr/bin/" + c); err == nil {
			if c == "apt-get" {
				return "apt"
			}
			return c
		}
	}
	return ""
}

var portLineRe = regexp.MustCompile(`(?m)^\s*Port\s+\d+\s*$`)

// sshConfigPorts returns the ports sshd is configured to listen on.
func sshConfigPorts() []int {
	seen := map[int]bool{}
	var ports []int
	files := []string{"/etc/ssh/sshd_config"}
	matches, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf")
	files = append(files, matches...)
	re := regexp.MustCompile(`(?m)^\s*Port\s+(\d+)\s*$`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			p, _ := strconv.Atoi(m[1])
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	if len(ports) == 0 {
		ports = []int{22}
	}
	return ports
}

// setSSHPorts rewrites sshd configuration so sshd listens exactly on the
// given ports. It strips Port lines everywhere and writes them to a
// drop-in (when the sshd_config.d include exists) or the main config.
func setSSHPorts(ports []int) error {
	files := []string{"/etc/ssh/sshd_config"}
	matches, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf")
	files = append(files, matches...)
	mainHasInclude := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if f == "/etc/ssh/sshd_config" && strings.Contains(string(b), "sshd_config.d") {
			mainHasInclude = true
		}
		cleaned := portLineRe.ReplaceAllString(string(b), "")
		if cleaned != string(b) {
			if err := os.WriteFile(f, []byte(cleaned), 0o600); err != nil {
				return err
			}
		}
	}
	var lines strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&lines, "Port %d\n", p)
	}
	if mainHasInclude {
		return os.WriteFile("/etc/ssh/sshd_config.d/00-vps-panel.conf", []byte(lines.String()), 0o600)
	}
	b, err := os.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return err
	}
	return os.WriteFile("/etc/ssh/sshd_config", []byte(lines.String()+string(b)), 0o600)
}

func sshSocketActivated(r *Runner) bool {
	out, err := r.Capture("systemctl", "is-enabled", "ssh.socket")
	if err == nil && strings.TrimSpace(out) == "enabled" {
		return true
	}
	out, err = r.Capture("systemctl", "is-active", "ssh.socket")
	return err == nil && strings.TrimSpace(out) == "active"
}

// setSSHSocketPorts writes a socket drop-in so ssh.socket listens on the
// given ports (Ubuntu socket-activation mode ignores sshd_config Port).
func setSSHSocketPorts(r *Runner, ports []int) error {
	dir := "/etc/systemd/system/ssh.socket.d"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("[Socket]\nListenStream=\n")
	for _, p := range ports {
		fmt.Fprintf(&sb, "ListenStream=%d\n", p)
	}
	if err := os.WriteFile(filepath.Join(dir, "10-vps-panel.conf"), []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return r.Run("systemctl", "daemon-reload")
}

func restartSSH(r *Runner) error {
	if currentInit() == initOpenRC {
		return r.Run("rc-service", "sshd", "restart")
	}
	if sshSocketActivated(r) {
		_ = r.Run("systemctl", "restart", "ssh.socket")
	}
	if err := r.Run("systemctl", "restart", "ssh"); err != nil {
		return r.Run("systemctl", "restart", "sshd")
	}
	return nil
}

func portListening(r *Runner, port int) bool {
	out, err := r.Capture("ss", "-tln")
	if err != nil {
		return false
	}
	return strings.Contains(out, fmt.Sprintf(":%d ", port)) || strings.Contains(out, fmt.Sprintf(":%d\t", port))
}

func writeFile(path, content string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), perm)
}

// stepSelected reports whether the named template step runs. An empty
// Steps list selects everything (the full template), which keeps older
// callers — notably the automatic post-onboarding init — unchanged.
func stepSelected(steps []string, id string) bool {
	if len(steps) == 0 {
		return true
	}
	for _, s := range steps {
		if s == id {
			return true
		}
	}
	return false
}

// --- the initialization template ---------------------------------------------

// RunInit executes the built-in VPS initialization template. The safe SSH
// port transition order is enforced here: firewall first, sshd second,
// verification third; the old port is only closed later by RunFinalizeSSH
// after the panel confirms the new port is reachable.
//
// p.Steps selects which steps run (empty = all); deselected steps are
// logged as 「未选择，跳过：…」 and left untouched.
func RunInit(p proto.InitParams, r *Runner) (map[string]any, error) {
	alpine := isAlpine()
	result := map[string]any{}
	sel := func(id string) bool { return stepSelected(p.Steps, id) }
	skip := func(name string) { r.logf("未选择，跳过：%s", name) }
	pm := pkgManager(r)
	if pm == "" && !alpine {
		r.Warn("未识别的包管理器（仅支持 apt/dnf/yum），跳过软件包相关步骤")
	}
	if alpine {
		r.Step("检测到 Alpine Linux：发行版专属步骤将逐项跳过并注明")
	}
	oldPorts := sshConfigPorts()
	result["old_ssh_port"] = oldPorts[0]

	// 1a. system update (its own selectable step)
	if !sel(proto.InitStepUpdate) {
		skip("系统更新")
	} else if alpine {
		r.logf("Alpine 跳过：系统更新（apt/dnf/yum）（后续 apk 化）")
	} else if pm == "apt" {
		r.Step("更新软件包")
		if err := r.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update"); err != nil {
			r.Warn("apt-get update 失败：%v", err)
		}
		if err := r.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "-y", "upgrade"); err != nil {
			r.Warn("apt-get upgrade 失败：%v", err)
		}
	} else if pm == "dnf" || pm == "yum" {
		r.Step("更新软件包")
		if err := r.Run(pm, "-y", "upgrade", "--refresh"); err != nil {
			r.Warn("%s upgrade 失败：%v", pm, err)
		}
	}

	// 1b. tools installation (its own selectable step). The ufw and
	// fail2ban packages ride along with their own steps: selecting only
	// the feature still installs its package (without the base tools).
	if !sel(proto.InitStepTools) {
		skip("常用工具")
	}
	if sel(proto.InitStepTools) || sel(proto.InitStepUFW) || sel(proto.InitStepFail2ban) {
		if alpine {
			if sel(proto.InitStepTools) {
				r.logf("Alpine 跳过：常用工具安装（后续 apk 化）")
			}
		} else if pm == "apt" {
			var pkgs []string
			if sel(proto.InitStepTools) {
				pkgs = append(pkgs, "curl", "wget", "vim", "unzip", "socat", "htop", "iproute2", "iputils-ping", "dnsutils", "ca-certificates")
			}
			if sel(proto.InitStepUFW) {
				pkgs = append(pkgs, "ufw")
			}
			if sel(proto.InitStepFail2ban) {
				pkgs = append(pkgs, "fail2ban")
			}
			if sel(proto.InitStepTools) {
				r.Step("安装常用工具")
			} else {
				r.Step("安装所选组件的软件包（%s）", strings.Join(pkgs, " "))
			}
			args := append([]string{"-y", "install"}, pkgs...)
			if err := r.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", args...); err != nil {
				r.Warn("安装工具失败：%v", err)
			}
		} else if pm == "dnf" || pm == "yum" {
			var pkgs []string
			if sel(proto.InitStepTools) {
				pkgs = append(pkgs, "curl", "wget", "vim-enhanced", "unzip", "socat", "htop", "iproute", "iputils", "bind-utils", "ca-certificates")
			}
			if sel(proto.InitStepFail2ban) {
				pkgs = append(pkgs, "fail2ban")
			}
			if len(pkgs) > 0 {
				if sel(proto.InitStepTools) {
					r.Step("安装常用工具")
				} else {
					r.Step("安装所选组件的软件包（%s）", strings.Join(pkgs, " "))
				}
				args := append([]string{"-y", "install"}, pkgs...)
				if err := r.Run(pm, args...); err != nil {
					r.Warn("安装工具失败：%v", err)
				}
			}
			if sel(proto.InitStepUFW) {
				if err := r.Run(pm, "-y", "install", "ufw"); err != nil {
					r.Warn("ufw 在该发行版不可用：%v", err)
				}
			}
		}
	}

	// 2a. timezone (its own selectable step)
	if !sel(proto.InitStepTimezone) {
		skip("时区")
	} else if p.Timezone != "" && alpine {
		r.Step("设置时区为 %s", p.Timezone)
		zonePath := "/usr/share/zoneinfo/" + p.Timezone
		if _, err := os.Stat(zonePath); err == nil {
			_ = r.Run("ln", "-sf", zonePath, "/etc/localtime")
			if !r.DryRun {
				_ = writeFile("/etc/timezone", p.Timezone+"\n", 0o644)
			}
		} else {
			r.logf("Alpine 跳过：时区设置（/usr/share/zoneinfo 不存在，缺 tzdata）（后续 apk 化）")
		}
	} else if p.Timezone != "" {
		r.Step("设置时区为 %s", p.Timezone)
		if err := r.Run("timedatectl", "set-timezone", p.Timezone); err != nil {
			if err2 := writeFile("/etc/timezone", p.Timezone+"\n", 0o644); err2 == nil {
				_ = r.Run("ln", "-sf", "/usr/share/zoneinfo/"+p.Timezone, "/etc/localtime")
			} else {
				r.Warn("设置时区失败：%v", err)
			}
		}
	}

	// 2b. time sync (its own selectable step)
	if !sel(proto.InitStepTimesync) {
		skip("时间同步")
	} else if alpine {
		r.logf("Alpine 跳过：时间同步服务启用（systemd-timesyncd/chronyd）（后续 apk 化）")
	} else if pm == "dnf" || pm == "yum" {
		r.Step("启用时间同步")
		if err := r.Run("systemctl", "enable", "--now", "chronyd"); err != nil {
			r.Warn("启用 chronyd 失败：%v", err)
		}
	} else {
		r.Step("启用时间同步")
		if err := r.Run("systemctl", "enable", "--now", "systemd-timesyncd"); err != nil {
			r.Warn("启用 systemd-timesyncd 失败：%v", err)
		}
	}

	// 3. ufw: allow current + new SSH ports BEFORE touching sshd
	wantPorts := append([]int{}, oldPorts...)
	if p.NewSSHPort > 0 && !contains(wantPorts, p.NewSSHPort) {
		wantPorts = append(wantPorts, p.NewSSHPort)
	}
	if !sel(proto.InitStepUFW) {
		skip("ufw 防火墙")
	} else {
		r.Step("配置 ufw 防火墙（先放行 SSH 端口 %v）", wantPorts)
		if alpine {
			r.logf("Alpine 跳过：ufw 防火墙配置（Alpine 无 ufw；请在服务商面板确认 SSH 端口 %v 已放行/映射）（后续 apk 化）", wantPorts)
		} else if _, err := r.Capture("which", "ufw"); err == nil || r.DryRun {
			_ = r.Run("ufw", "default", "deny", "incoming")
			_ = r.Run("ufw", "default", "allow", "outgoing")
			for _, port := range wantPorts {
				_ = r.Run("ufw", "allow", fmt.Sprintf("%d/tcp", port))
			}
			if err := r.Run("ufw", "--force", "enable"); err != nil {
				r.Warn("启用 ufw 失败：%v", err)
			}
		} else {
			r.Warn("ufw 不可用，跳过防火墙配置（协议端口将不会被 ufw 拦截）")
		}
	}

	// 4. fail2ban for sshd
	if !sel(proto.InitStepFail2ban) {
		skip("fail2ban")
	} else {
		r.Step("配置 fail2ban 防护 SSH")
		if alpine {
			r.logf("Alpine 跳过：fail2ban 安装与配置（后续 apk 化）")
		} else {
			portList := make([]string, len(wantPorts))
			for i, p := range wantPorts {
				portList[i] = strconv.Itoa(p)
			}
			jail := "[sshd]\nenabled = true\nport = " + strings.Join(portList, ",") + "\nmaxretry = 5\nfindtime = 10m\nbantime = 1h\n"
			if err := writeFile("/etc/fail2ban/jail.d/vps-panel-sshd.conf", jail, 0o644); err != nil && !r.DryRun {
				r.Warn("写入 fail2ban 配置失败：%v", err)
			}
			if err := r.Run("systemctl", "enable", "--now", "fail2ban"); err != nil {
				r.Warn("启用 fail2ban 失败：%v", err)
			} else {
				_ = r.Run("systemctl", "restart", "fail2ban")
			}
		}
	}

	// 5. SSH port change: transition with both ports listening
	if !sel(proto.InitStepSSHPort) {
		if p.NewSSHPort > 0 {
			r.logf("未选择，跳过：修改 SSH 端口（已忽略指定的新端口 %d）", p.NewSSHPort)
		} else {
			skip("修改 SSH 端口")
		}
	} else if p.NewSSHPort > 0 && !contains(oldPorts, p.NewSSHPort) {
		r.Step("SSH 端口过渡：同时监听旧端口 %v 与新端口 %d", oldPorts, p.NewSSHPort)
		if !r.DryRun {
			if err := setSSHPorts(wantPorts); err != nil {
				return result, fmt.Errorf("写入 sshd 配置失败： %w", err)
			}
		} else {
			r.logf("[dry-run] sshd 将监听端口 %v", wantPorts)
		}
		// SELinux: allow sshd on the new port where semanage exists
		if _, err := r.Capture("which", "semanage"); err == nil {
			_ = r.Run("semanage", "port", "-a", "-t", "ssh_port_t", "-p", "tcp", strconv.Itoa(p.NewSSHPort))
		}
		if currentInit() == initSystemd && sshSocketActivated(r) {
			r.logf("检测到 ssh.socket 套接字激活，同步写入 socket 端口")
			if err := setSSHSocketPorts(r, wantPorts); err != nil && !r.DryRun {
				r.Warn("写入 ssh.socket 端口失败：%v", err)
			}
		}
		if err := restartSSH(r); err != nil {
			return result, fmt.Errorf("重启 SSH 服务失败（旧端口配置仍保留）： %w", err)
		}
		if !r.DryRun {
			ok := false
			for i := 0; i < 5; i++ {
				if portListening(r, p.NewSSHPort) {
					ok = true
					break
				}
				_ = r.Run("sleep", "1")
			}
			if !ok {
				return result, fmt.Errorf("新 SSH 端口 %d 未在监听（旧端口仍保留，可继续排查）", p.NewSSHPort)
			}
		}
		r.logf("新 SSH 端口 %d 已监听；等面板侧验证可达后将关闭旧端口", p.NewSSHPort)
		result["new_ssh_port"] = p.NewSSHPort
		result["pending_finalize"] = true
	}

	// 6. BBR
	if !sel(proto.InitStepBBR) {
		skip("BBR")
	} else {
		r.Step("开启 BBR")
		_ = r.Run("modprobe", "tcp_bbr")
		if err := writeFile("/etc/sysctl.d/99-vps-panel-bbr.conf", "net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n", 0o644); err != nil && !r.DryRun {
			r.Warn("写入 BBR 配置失败：%v", err)
		}
		_ = r.Run("sysctl", "-p", "/etc/sysctl.d/99-vps-panel-bbr.conf")
		if cur, err := r.Capture("sysctl", "-n", "net.ipv4.tcp_congestion_control"); err == nil && cur != "" {
			r.logf("当前拥塞控制算法：%s", cur)
		}
	}

	// 7. swap for small-memory hosts without swap
	if !sel(proto.InitStepSwap) {
		skip("swap")
	} else {
		memTotal, _, swapTotal, _ := memInfo()
		if swapTotal == 0 && memTotal > 0 && memTotal <= 2048*1024*1024 {
			sizeMB := int(memTotal / 1024 / 1024)
			if sizeMB < 512 {
				sizeMB = 512
			}
			if sizeMB > 2048 {
				sizeMB = 2048
			}
			r.Step("小内存无 swap，创建 %dMB swap", sizeMB)
			if _, err := os.Stat("/swapfile"); os.IsNotExist(err) || r.DryRun {
				if err := r.RunShell(fmt.Sprintf("fallocate -l %dM /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=%d", sizeMB, sizeMB)); err != nil {
					r.Warn("创建 swapfile 失败：%v", err)
				} else {
					_ = r.Run("chmod", "600", "/swapfile")
					if err := r.Run("mkswap", "/swapfile"); err != nil {
						r.Warn("mkswap 失败：%v", err)
					} else if err := r.Run("swapon", "/swapfile"); err != nil {
						r.Warn("swapon 失败：%v", err)
					} else {
						_ = r.RunShell("grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab")
						_ = writeFile("/etc/sysctl.d/99-vps-panel-swap.conf", "vm.swappiness=10\n", 0o644)
						_ = r.Run("sysctl", "-p", "/etc/sysctl.d/99-vps-panel-swap.conf")
					}
				}
			}
		} else {
			r.logf("跳过 swap（内存 %dMB，swap %dMB）", memTotal/1024/1024, swapTotal/1024/1024)
		}
	}

	// 8. agent autostart
	if !sel(proto.InitStepAgentAutostart) {
		skip("agent 开机自启")
	} else {
		r.Step("确认 agent 开机自启")
		switch currentInit() {
		case initOpenRC:
			if err := r.Run("rc-update", "add", "vps-agent", "default"); err != nil {
				r.Warn("启用 vps-agent 自启失败（OpenRC）：%v", err)
			}
		case initNone:
			r.Warn("无 systemd/OpenRC，无法设置 vps-agent 开机自启")
		default:
			if err := r.Run("systemctl", "enable", "vps-agent"); err != nil {
				r.Warn("启用 vps-agent 自启失败：%v", err)
			}
		}
	}

	r.logf("VPS初始化执行完成")
	return result, nil
}

// RunFinalizeSSH closes the old SSH port after the panel has verified the
// new one is reachable from outside.
func RunFinalizeSSH(p proto.FinalizeParams, r *Runner) error {
	r.Step("收尾：关闭旧 SSH 端口 %d，仅保留 %d", p.OldSSHPort, p.NewSSHPort)
	if !r.DryRun {
		if err := setSSHPorts([]int{p.NewSSHPort}); err != nil {
			return fmt.Errorf("写入 sshd 配置失败： %w", err)
		}
	} else {
		r.logf("[dry-run] sshd 将仅监听端口 %d", p.NewSSHPort)
	}
	if currentInit() == initSystemd && sshSocketActivated(r) {
		if err := setSSHSocketPorts(r, []int{p.NewSSHPort}); err != nil && !r.DryRun {
			r.Warn("写入 ssh.socket 端口失败：%v", err)
		}
	}
	if err := restartSSH(r); err != nil {
		return fmt.Errorf("重启 SSH 服务失败： %w", err)
	}
	// firewall + fail2ban follow the new port
	_ = r.Run("ufw", "delete", "allow", fmt.Sprintf("%d/tcp", p.OldSSHPort))
	jail := fmt.Sprintf("[sshd]\nenabled = true\nport = %d\nmaxretry = 5\nfindtime = 10m\nbantime = 1h\n", p.NewSSHPort)
	if err := writeFile("/etc/fail2ban/jail.d/vps-panel-sshd.conf", jail, 0o644); err != nil && !r.DryRun {
		r.Warn("更新 fail2ban 配置失败：%v", err)
	}
	if currentInit() == initOpenRC {
		_ = r.Run("rc-service", "fail2ban", "restart")
	} else {
		_ = r.Run("systemctl", "restart", "fail2ban")
	}
	r.logf("旧 SSH 端口 %d 已关闭", p.OldSSHPort)
	return nil
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

package agent

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hoolu0/vps-panel/internal/proto"
)

const singBoxBin = "/usr/local/bin/sing-box"

// fallbackSingBoxVersion is used to build a direct download URL when the
// GitHub "latest release" API lookup fails (rate limits etc.).
const fallbackSingBoxVersion = "1.14.4"

// StoredDeployment is a deployment as persisted by the agent. It carries
// the Reality private key, which never leaves the agent.
type StoredDeployment struct {
	proto.Deployment
	PrivateKey string `json:"private_key,omitempty"`
}

// Public returns the deployment as reported to the panel (no secrets that
// must stay local; the SS password and VLESS UUID are connection params the
// panel needs for share links).
func (d StoredDeployment) Public() proto.Deployment { return d.Deployment }

// SingBox manages the local sing-box installation and its deployments.
type SingBox struct {
	DataDir string
	deps    []StoredDeployment
}

func (s *SingBox) depsPath() string   { return filepath.Join(s.DataDir, "deployments.json") }
func (s *SingBox) configPath() string { return filepath.Join(s.DataDir, "sing-box.json") }

// Load reads persisted deployments from disk.
func (s *SingBox) Load() {
	b, err := os.ReadFile(s.depsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.deps)
}

func (s *SingBox) save() error {
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.deps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.depsPath(), b, 0o600)
}

// List returns all deployments in panel-facing form.
func (s *SingBox) List() []proto.Deployment {
	out := make([]proto.Deployment, 0, len(s.deps))
	for _, d := range s.deps {
		out = append(out, d.Public())
	}
	return out
}

func (s *SingBox) find(id string) int {
	for i, d := range s.deps {
		if d.ID == id {
			return i
		}
	}
	return -1
}

func genUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func genRealityKeypair() (priv, pub string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	priv = base64.RawURLEncoding.EncodeToString(key.Bytes())
	pub = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	return priv, pub, nil
}

func genShortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func genSSPassword() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func (s *SingBox) pickPort() int {
	for i := 0; i < 50; i++ {
		var b [2]byte
		_, _ = rand.Read(b[:])
		port := 10000 + int(b[0])<<8 | int(b[1])%50000
		if port > 65535 {
			port = 10000 + port%50000
		}
		used := false
		for _, d := range s.deps {
			if d.Port == port {
				used = true
			}
		}
		if !used && portFree(port) {
			return port
		}
	}
	return 0
}

// --- installation -----------------------------------------------------------

func (s *SingBox) ensureInstalled(r *Runner) error {
	if _, err := os.Stat(singBoxBin); err == nil {
		return nil
	}
	r.Step("安装 sing-box")
	version, url, err := latestReleaseAsset()
	if err != nil {
		// GitHub API 未鉴权查询容易被限流（共享出口 IP 尤甚）：查询失败不
		// 直接报错，改用已验证的固定版本直链下载（不走 API）。
		r.logf("查询 sing-box 最新版本失败（%v），改用固定版本 %s", err, fallbackSingBoxVersion)
		version = "v" + fallbackSingBoxVersion
		url = fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%[1]s/sing-box-%[1]s-linux-%[2]s.tar.gz", fallbackSingBoxVersion, runtime.GOARCH)
	}
	r.logf("下载 sing-box %s (%s)", version, runtime.GOARCH)
	tmp, err := os.MkdirTemp("", "singbox-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tgz := filepath.Join(tmp, "sb.tar.gz")
	if err := downloadFile(url, tgz); err != nil {
		return fmt.Errorf("下载 sing-box 失败： %w", err)
	}
	if err := extractSingBox(tgz, tmp); err != nil {
		return err
	}
	binPath := filepath.Join(tmp, "sing-box")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("压缩包内未找到 sing-box 二进制")
	}
	data, err := os.ReadFile(binPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(singBoxBin, data, 0o755); err != nil {
		return err
	}
	r.logf("sing-box %s 已安装到 %s", version, singBoxBin)
	return s.ensureService(r)
}

func (s *SingBox) ensureService(r *Runner) error {
	unit := `[Unit]
Description=sing-box service
After=network.target nss-lookup.target

[Service]
ExecStart=` + singBoxBin + ` run -c ` + s.configPath() + `
Restart=on-failure
RestartSec=3
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`
	unitPath := "/etc/systemd/system/sing-box.service"
	cur, _ := os.ReadFile(unitPath)
	if string(cur) != unit {
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return err
		}
		if err := r.Run("systemctl", "daemon-reload"); err != nil {
			r.Warn("systemctl daemon-reload 失败：%v", err)
		}
	}
	_ = r.Run("systemctl", "enable", "sing-box")
	return nil
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func latestReleaseAsset() (version, url string, err error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/SagerNet/sing-box/releases/latest", nil)
	req.Header.Set("User-Agent", "vps-panel-agent")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}
	want := "linux-" + runtime.GOARCH + ".tar.gz"
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, want) && strings.HasPrefix(a.Name, "sing-box-") {
			return rel.TagName, a.URL, nil
		}
	}
	return "", "", fmt.Errorf("release %s 中没有 %s 资源", rel.TagName, want)
}

func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func extractSingBox(tgz, dstDir string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) == "sing-box" && hdr.Typeflag == tar.TypeReg {
			out, err := os.Create(filepath.Join(dstDir, "sing-box"))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
	}
	return fmt.Errorf("tar 中没有 sing-box 文件")
}

// --- config -----------------------------------------------------------------

func splitHostPort(s string, defPort int) (string, int) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, defPort
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return h, defPort
	}
	return h, port
}

func (s *SingBox) buildConfig() ([]byte, error) {
	inbounds := make([]map[string]any, 0, len(s.deps))
	for _, d := range s.deps {
		switch d.Module {
		case proto.ModuleVLESSReality:
			destHost, destPort := splitHostPort(d.Dest, 443)
			inbounds = append(inbounds, map[string]any{
				"type":        "vless",
				"tag":         d.ID,
				"listen":      "::",
				"listen_port": d.Port,
				"users": []map[string]any{
					{"uuid": d.UUID, "flow": "xtls-rprx-vision"},
				},
				"tls": map[string]any{
					"enabled":     true,
					"server_name": d.ServerName,
					"reality": map[string]any{
						"enabled": true,
						"handshake": map[string]any{
							"server":      destHost,
							"server_port": destPort,
						},
						"private_key": d.PrivateKey,
						"short_id":    []string{d.ShortID},
					},
				},
			})
		case proto.ModuleSS2022:
			inbounds = append(inbounds, map[string]any{
				"type":        "shadowsocks",
				"tag":         d.ID,
				"listen":      "::",
				"listen_port": d.Port,
				"method":      d.Method,
				"password":    d.Password,
				"network":     []string{"tcp", "udp"},
			})
		}
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": true},
		"inbounds":  inbounds,
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// applyConfig validates the new config against a temp file first and only
// then atomically replaces the live config and restarts sing-box. A bad
// config therefore never reaches the live path (the running service keeps
// its previous configuration).
func (s *SingBox) applyConfig(r *Runner) error {
	cfg, err := s.buildConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	if r.DryRun {
		r.logf("[dry-run] 已生成 sing-box 配置（%d 个入站）", len(s.deps))
		return nil
	}
	tmpPath := s.configPath() + ".tmp"
	if err := os.WriteFile(tmpPath, cfg, 0o600); err != nil {
		return err
	}
	out, err := (&Runner{Log: r.Log}).CaptureCombined(singBoxBin, "check", "-c", tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sing-box 配置校验失败： %v %s", err, out)
	}
	if err := os.Rename(tmpPath, s.configPath()); err != nil {
		return err
	}
	if err := r.Run("systemctl", "restart", "sing-box"); err != nil {
		return fmt.Errorf("重启 sing-box 失败： %w", err)
	}
	r.logf("sing-box 已重启，当前 %d 个入站", len(s.deps))
	return nil
}

func (s *SingBox) ufwAllow(r *Runner, port int, udp bool) {
	if r.DryRun {
		r.logf("[dry-run] ufw allow %d", port)
		return
	}
	status, err := (&Runner{}).Capture("ufw", "status")
	if err != nil || !strings.Contains(status, "Status: active") {
		return // ufw absent or inactive: nothing to do
	}
	_ = r.Run("ufw", "allow", fmt.Sprintf("%d/tcp", port))
	if udp {
		_ = r.Run("ufw", "allow", fmt.Sprintf("%d/udp", port))
	}
}

func (s *SingBox) ufwDelete(r *Runner, port int) {
	if r.DryRun {
		r.logf("[dry-run] ufw delete allow %d", port)
		return
	}
	status, err := (&Runner{}).Capture("ufw", "status")
	if err != nil || !strings.Contains(status, "Status: active") {
		return
	}
	_ = r.Run("ufw", "delete", "allow", fmt.Sprintf("%d/tcp", port))
	_ = r.Run("ufw", "delete", "allow", fmt.Sprintf("%d/udp", port))
}

// --- task operations --------------------------------------------------------

// Deploy installs or updates one deployment and returns its public form.
func (s *SingBox) Deploy(p proto.DeployParams, r *Runner) (proto.Deployment, error) {
	if p.Module != proto.ModuleVLESSReality && p.Module != proto.ModuleSS2022 {
		return proto.Deployment{}, fmt.Errorf("未知协议模块 %q", p.Module)
	}
	if !r.DryRun {
		if err := s.ensureInstalled(r); err != nil {
			return proto.Deployment{}, err
		}
		if err := s.ensureService(r); err != nil {
			r.Warn("写入 sing-box systemd 服务失败：%v", err)
		}
	}

	idx := s.find(p.DeploymentID)
	var d StoredDeployment
	if idx >= 0 && s.deps[idx].Module == p.Module {
		d = s.deps[idx] // keep existing credentials on update
	} else {
		d = StoredDeployment{}
		d.ID = p.DeploymentID
		d.Module = p.Module
		d.CreatedAt = time.Now()
		switch p.Module {
		case proto.ModuleVLESSReality:
			priv, pub, err := genRealityKeypair()
			if err != nil {
				return proto.Deployment{}, err
			}
			d.UUID = genUUID()
			d.PublicKey = pub
			d.PrivateKey = priv
			d.ShortID = genShortID()
		case proto.ModuleSS2022:
			d.Method = "2022-blake3-aes-128-gcm"
			d.Password = genSSPassword()
		}
	}
	if p.Module == proto.ModuleVLESSReality {
		d.Dest = p.Dest
		if d.Dest == "" {
			d.Dest = "www.microsoft.com:443"
		}
		d.ServerName = p.ServerName
		if d.ServerName == "" {
			host, _ := splitHostPort(d.Dest, 443)
			d.ServerName = host
		}
	}
	if p.Port > 0 {
		d.Port = p.Port
	} else if d.Port == 0 {
		d.Port = s.pickPort()
		if d.Port == 0 {
			return proto.Deployment{}, fmt.Errorf("无法分配空闲端口")
		}
	}

	old := s.deps
	if idx >= 0 {
		s.deps[idx] = d
	} else {
		s.deps = append(s.deps, d)
	}
	if err := s.applyConfig(r); err != nil {
		s.deps = old // roll back in-memory state; config on disk may differ but check failed before restart
		return proto.Deployment{}, err
	}
	if err := s.save(); err != nil {
		return proto.Deployment{}, err
	}
	s.ufwAllow(r, d.Port, p.Module == proto.ModuleSS2022)
	r.logf("部署完成：%s 端口 %d", d.Module, d.Port)
	return d.Public(), nil
}

// Undeploy removes one deployment.
func (s *SingBox) Undeploy(id string, r *Runner) error {
	idx := s.find(id)
	if idx < 0 {
		return fmt.Errorf("部署 %s 不存在", id)
	}
	port := s.deps[idx].Port
	old := s.deps
	s.deps = append(s.deps[:idx], s.deps[idx+1:]...)
	if err := s.applyConfig(r); err != nil {
		s.deps = old
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	s.ufwDelete(r, port)
	r.logf("已移除部署 %s（端口 %d）", id, port)
	return nil
}

// RestartService restarts sing-box.
func (s *SingBox) RestartService(r *Runner) error {
	if r.DryRun {
		r.logf("[dry-run] systemctl restart sing-box")
		return nil
	}
	return r.Run("systemctl", "restart", "sing-box")
}

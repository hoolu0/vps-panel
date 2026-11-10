package panel

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"github.com/hoolu0/vps-panel/internal/proto"
)

//go:embed web/*
var webFS embed.FS

// Server is the panel HTTP server.
type Server struct {
	store    *Store
	hub      *Hub
	sessMu   sync.Mutex
	sessions map[string]time.Time
}

// NewServer builds a panel Server.
func NewServer(store *Store) *Server {
	return &Server{store: store, hub: newHub(store), sessions: map[string]time.Time{}}
}

// SetAdminPassword hashes and stores the admin password.
func (s *Server) SetAdminPassword(pw string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.store.Update(func(d *Data) { d.AdminPasswordHash = string(h) })
}

// HasAdminPassword reports whether an admin password is configured.
func (s *Server) HasAdminPassword() bool {
	has := false
	s.store.View(func(d *Data) { has = d.AdminPasswordHash != "" })
	return has
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /agent/ws", s.handleAgentWS)

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /api/me", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": proto.Version})
	}))
	mux.HandleFunc("POST /api/password", s.auth(s.handlePassword))

	for _, p := range []string{"/api", "/api/v1"} {
		mux.HandleFunc("GET "+p+"/nodes", s.auth(s.handleListNodes))
		mux.HandleFunc("GET "+p+"/nodes/{id}", s.auth(s.handleGetNode))
		mux.HandleFunc("POST "+p+"/nodes/{id}/probe", s.auth(s.handleProbe))
		mux.HandleFunc("POST "+p+"/nodes/{id}/deploy", s.auth(s.handleDeploy))
		mux.HandleFunc("DELETE "+p+"/nodes/{id}/deployments/{dep}", s.auth(s.handleUndeploy))
		mux.HandleFunc("GET "+p+"/tasks/{id}", s.auth(s.handleGetTask))
	}
	// admin-only (session or token; token scope is fleet management too,
	// but node creation carries SSH credentials, so keep it on /api only)
	mux.HandleFunc("POST /api/nodes", s.auth(s.handleAddNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", s.auth(s.handleDeleteNode))
	mux.HandleFunc("POST /api/nodes/{id}/init", s.auth(s.handleInit))
	mux.HandleFunc("POST /api/nodes/{id}/finalize-ssh", s.auth(s.handleFinalizeSSH))
	mux.HandleFunc("POST /api/nodes/{id}/deployments/{dep}/restart", s.auth(s.handleRestartProto))
	mux.HandleFunc("GET /api/nodes/{id}/deployments/{dep}/qrcode", s.auth(s.handleQRCode))
	mux.HandleFunc("GET /api/nodes/{id}/route", s.auth(s.handleRouteSources))
	mux.HandleFunc("POST /api/nodes/{id}/route", s.auth(s.handleRouteQuery))
	mux.HandleFunc("GET /api/tasks", s.auth(s.handleListTasks))
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("GET /api/tokens", s.auth(s.handleListTokens))
	mux.HandleFunc("POST /api/tokens", s.auth(s.handleCreateToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.auth(s.handleDeleteToken))

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))
	return mux
}

// --- helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// --- auth -----------------------------------------------------------------------

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// session cookie
		if c, err := r.Cookie("vp_session"); err == nil {
			s.sessMu.Lock()
			exp, ok := s.sessions[c.Value]
			if ok && time.Now().After(exp) {
				delete(s.sessions, c.Value)
				ok = false
			}
			s.sessMu.Unlock()
			if ok {
				next(w, r)
				return
			}
		}
		// API token
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimPrefix(h, "Bearer ")
			hash := hashToken(tok)
			authed := false
			_ = s.store.Update(func(d *Data) {
				for _, t := range d.APITokens {
					if t.Hash == hash {
						authed = true
						if time.Since(t.LastUsedAt) > time.Minute {
							t.LastUsedAt = time.Now()
						}
					}
				}
			})
			if authed {
				next(w, r)
				return
			}
		}
		writeErr(w, 401, "unauthorized")
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	ok := false
	s.store.View(func(d *Data) {
		if d.AdminPasswordHash != "" {
			ok = bcrypt.CompareHashAndPassword([]byte(d.AdminPasswordHash), []byte(body.Password)) == nil
		}
	})
	if !ok {
		writeErr(w, 401, "密码不正确")
		return
	}
	sid := NewSecret(32)
	s.sessMu.Lock()
	s.sessions[sid] = time.Now().Add(7 * 24 * time.Hour)
	s.sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "vp_session", Value: sid, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 7 * 24 * 3600,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("vp_session"); err == nil {
		s.sessMu.Lock()
		delete(s.sessions, c.Value)
		s.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "vp_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := readJSON(r, &body); err != nil || len(body.New) < 6 {
		writeErr(w, 400, "新密码至少 6 位")
		return
	}
	ok := false
	s.store.View(func(d *Data) {
		ok = bcrypt.CompareHashAndPassword([]byte(d.AdminPasswordHash), []byte(body.Old)) == nil
	})
	if !ok {
		writeErr(w, 401, "原密码不正确")
		return
	}
	if err := s.SetAdminPassword(body.New); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- agent websocket --------------------------------------------------------------

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	var node *Node
	s.store.View(func(d *Data) { node = FindNodeByToken(d, token) })
	if node == nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ac := &agentConn{nodeID: node.ID, conn: conn}
	s.hub.register(ac)
	defer s.hub.unregister(ac)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	})
	for {
		var env proto.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return
		}
		switch env.Type {
		case proto.MsgHello:
			s.hub.onHello(node.ID, env.Data)
		case proto.MsgStatus:
			s.hub.onStatus(node.ID, env.Data)
		case proto.MsgDeployments:
			s.hub.onDeployments(node.ID, env.Data)
		case proto.MsgProbe:
			s.hub.onProbe(node.ID, env.Data)
		case proto.MsgTaskLog:
			s.hub.onTaskLog(node.ID, env.TaskID, env.Data)
		case proto.MsgTaskResult:
			s.hub.onTaskResult(node.ID, env.TaskID, env.Data)
		}
	}
}

// --- node views -------------------------------------------------------------------

func (s *Server) nodeView(n *Node) map[string]any {
	b, _ := json.Marshal(n)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "token") // agent credentials are never exposed via the API
	m["online"] = s.hub.Online(n.ID)
	deps := make([]map[string]any, 0, len(n.Deployments))
	for _, d := range n.Deployments {
		db, _ := json.Marshal(d)
		var dm map[string]any
		_ = json.Unmarshal(db, &dm)
		dm["link"] = ComposeLink(n, d)
		deps = append(deps, dm)
	}
	m["deployments"] = deps
	return m
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	s.store.View(func(d *Data) {
		for _, n := range d.Nodes {
			out = append(out, s.nodeView(n))
		}
	})
	writeJSON(w, 200, map[string]any{"nodes": out})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var out map[string]any
	s.store.View(func(d *Data) {
		if n := FindNode(d, id); n != nil {
			cp := *n
			out = s.nodeView(&cp)
		}
	})
	if out == nil {
		writeErr(w, 404, "node not found")
		return
	}
	writeJSON(w, 200, out)
}

// --- onboarding -------------------------------------------------------------------

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	var req OnboardRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if req.Name == "" || req.IP == "" || req.SSHPort == 0 {
		writeErr(w, 400, "name、ip、ssh_port 必填")
		return
	}
	if req.AuthType == "key" && req.PrivateKey == "" {
		writeErr(w, 400, "密钥登录需提供私钥")
		return
	}
	if req.AuthType != "key" && req.Password == "" {
		writeErr(w, 400, "密码登录需提供密码")
		return
	}
	node := &Node{
		ID: NewID("node-"), Name: req.Name, IP: req.IP, SSHPort: req.SSHPort,
		Token: NewSecret(24), CreatedAt: time.Now(),
		Timezone: req.Timezone, NewSSHPort: req.NewSSHPort,
	}
	if err := s.store.Update(func(d *Data) { d.Nodes = append(d.Nodes, node) }); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	panelBase := s.panelBase(r)
	logs, err := onboardSSH(panelBase, node, req)
	if err != nil {
		_ = s.store.Update(func(d *Data) {
			for i, n := range d.Nodes {
				if n.ID == node.ID {
					d.Nodes = append(d.Nodes[:i], d.Nodes[i+1:]...)
				}
			}
		})
		writeJSON(w, 502, map[string]any{"error": err.Error(), "logs": logs})
		return
	}
	if req.RunInit {
		_, _ = s.hub.Dispatch(node.ID, proto.TaskInit, proto.InitParams{
			Timezone:   req.Timezone,
			NewSSHPort: req.NewSSHPort,
		})
	}
	writeJSON(w, 200, map[string]any{"node": s.nodeView(node), "logs": logs})
}

// panelBase derives the panel URL agents should use: the configured
// public address when set, otherwise the address of this request.
func (s *Server) panelBase(r *http.Request) string {
	base := ""
	s.store.View(func(d *Data) { base = d.PublicAddr })
	if base != "" {
		return strings.TrimSuffix(base, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.store.Update(func(d *Data) {
		for i, n := range d.Nodes {
			if n.ID == id {
				d.Nodes = append(d.Nodes[:i], d.Nodes[i+1:]...)
			}
		}
		kept := d.Tasks[:0]
		for _, t := range d.Tasks {
			if t.NodeID != id {
				kept = append(kept, t)
			}
		}
		d.Tasks = kept
	})
	if c := s.hub.get(id); c != nil {
		_ = c.conn.Close()
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- node actions -------------------------------------------------------------------

func (s *Server) dispatchOrErr(w http.ResponseWriter, nodeID, kind string, params any) {
	rec, err := s.hub.Dispatch(nodeID, kind, params)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"task": rec})
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Timezone   string `json:"timezone"`
		NewSSHPort int    `json:"new_ssh_port"`
		DryRun     bool   `json:"dry_run"`
	}
	_ = readJSON(r, &body)
	s.store.View(func(d *Data) {
		if n := FindNode(d, id); n != nil {
			if body.Timezone == "" {
				body.Timezone = n.Timezone
			}
			if body.NewSSHPort == 0 {
				body.NewSSHPort = n.NewSSHPort
			}
		}
	})
	s.dispatchOrErr(w, id, proto.TaskInit, proto.InitParams{Timezone: body.Timezone, NewSSHPort: body.NewSSHPort, DryRun: body.DryRun})
}

func (s *Server) handleFinalizeSSH(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var node *Node
	s.store.View(func(d *Data) { node = FindNode(d, id) })
	if node == nil {
		writeErr(w, 404, "node not found")
		return
	}
	if node.NewSSHPort == 0 || node.NewSSHPort == node.SSHPort {
		writeErr(w, 400, "该节点没有待收尾的新 SSH 端口")
		return
	}
	s.dispatchOrErr(w, id, proto.TaskFinalizeSSH, proto.FinalizeParams{
		NewSSHPort: node.NewSSHPort, OldSSHPort: node.SSHPort,
	})
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	s.dispatchOrErr(w, r.PathValue("id"), proto.TaskProbeNow, nil)
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Module     string `json:"module"`
		Port       int    `json:"port"`
		Dest       string `json:"dest"`
		ServerName string `json:"server_name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if body.Module != proto.ModuleVLESSReality && body.Module != proto.ModuleSS2022 {
		writeErr(w, 400, "不支持的协议模块")
		return
	}
	s.dispatchOrErr(w, id, proto.TaskDeploy, proto.DeployParams{
		DeploymentID: NewID("dep-"), Module: body.Module, Port: body.Port,
		Dest: body.Dest, ServerName: body.ServerName,
	})
}

func (s *Server) handleUndeploy(w http.ResponseWriter, r *http.Request) {
	s.dispatchOrErr(w, r.PathValue("id"), proto.TaskUndeploy, proto.UndeployParams{DeploymentID: r.PathValue("dep")})
}

func (s *Server) handleRestartProto(w http.ResponseWriter, r *http.Request) {
	s.dispatchOrErr(w, r.PathValue("id"), proto.TaskRestartProto, nil)
}

func (s *Server) handleQRCode(w http.ResponseWriter, r *http.Request) {
	id, depID := r.PathValue("id"), r.PathValue("dep")
	var link string
	s.store.View(func(d *Data) {
		if n := FindNode(d, id); n != nil {
			for _, dep := range n.Deployments {
				if dep.ID == depID {
					link = ComposeLink(n, dep)
				}
			}
		}
	})
	if link == "" {
		writeErr(w, 404, "deployment not found")
		return
	}
	qr, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline; filename="+sanitizeFileName(depID)+".png")
	_ = png.Encode(w, qrImage(qr.Bitmap(), 8))
}

// qrImage renders a QR bitmap ([][]bool) to an image with the given scale.
func qrImage(bitmap [][]bool, scale int) image.Image {
	n := len(bitmap)
	img := image.NewRGBA(image.Rect(0, 0, n*scale, n*scale))
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			c := white
			if bitmap[y][x] {
				c = black
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	return img
}

// --- tasks ---------------------------------------------------------------------------

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	out := []*TaskRec{}
	s.store.View(func(d *Data) {
		for _, t := range d.Tasks {
			if nodeID == "" || t.NodeID == nodeID {
				out = append(out, t)
			}
		}
	})
	if len(out) > 100 {
		out = out[:100]
	}
	writeJSON(w, 200, map[string]any{"tasks": out})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var out *TaskRec
	s.store.View(func(d *Data) { out = FindTask(d, id) })
	if out == nil {
		writeErr(w, 404, "task not found")
		return
	}
	writeJSON(w, 200, out)
}

// --- settings / tokens / routes ---------------------------------------------------------

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	var out map[string]any
	s.store.View(func(d *Data) {
		out = map[string]any{
			"public_addr":        d.PublicAddr,
			"probe_targets":      d.ProbeTargets,
			"probe_interval_min": d.ProbeIntervalMin,
			"route_sources":      d.RouteSources,
		}
	})
	writeJSON(w, 200, out)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicAddr       string              `json:"public_addr"`
		ProbeTargets     []proto.ProbeTarget `json:"probe_targets"`
		ProbeIntervalMin int                 `json:"probe_interval_min"`
		RouteSources     []RouteSource       `json:"route_sources"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	for i := range body.RouteSources {
		if body.RouteSources[i].ID == "" {
			body.RouteSources[i].ID = NewID("rs-")
		}
	}
	if body.ProbeIntervalMin <= 0 {
		body.ProbeIntervalMin = 5
	}
	_ = s.store.Update(func(d *Data) {
		d.PublicAddr = body.PublicAddr
		d.ProbeTargets = body.ProbeTargets
		d.ProbeIntervalMin = body.ProbeIntervalMin
		d.RouteSources = body.RouteSources
	})
	s.hub.PushProbeConfig("")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	out := []*APIToken{}
	s.store.View(func(d *Data) { out = append(out, d.APITokens...) })
	writeJSON(w, 200, map[string]any{"tokens": out})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = readJSON(r, &body)
	if body.Name == "" {
		body.Name = "token"
	}
	raw := "vp_" + NewSecret(24)
	rec := &APIToken{
		ID: NewID("tok-"), Name: body.Name, Hash: hashToken(raw),
		Prefix: raw[:10], CreatedAt: time.Now(),
	}
	_ = s.store.Update(func(d *Data) { d.APITokens = append(d.APITokens, rec) })
	writeJSON(w, 200, map[string]any{"token": raw, "record": rec})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.store.Update(func(d *Data) {
		for i, t := range d.APITokens {
			if t.ID == id {
				d.APITokens = append(d.APITokens[:i], d.APITokens[i+1:]...)
			}
		}
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRouteSources(w http.ResponseWriter, r *http.Request) {
	out := []RouteSource{}
	s.store.View(func(d *Data) { out = append(out, d.RouteSources...) })
	writeJSON(w, 200, map[string]any{"route_sources": out})
}

// handleRouteQuery fetches the outbound-route （去程） data for a node from a
// configured pluggable data source (URL template with {ip}).
func (s *Server) handleRouteQuery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		SourceID string `json:"source_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	var node *Node
	var src *RouteSource
	s.store.View(func(d *Data) {
		node = FindNode(d, id)
		for i, rs := range d.RouteSources {
			if rs.ID == body.SourceID {
				src = &d.RouteSources[i]
			}
		}
	})
	if node == nil {
		writeErr(w, 404, "node not found")
		return
	}
	if src == nil {
		writeErr(w, 400, "请先在设置中添加线路数据源")
		return
	}
	target := strings.ReplaceAll(src.URLTemplate, "{ip}", node.IP)
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("User-Agent", "vps-panel/"+proto.Version)
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, 502, "数据源请求失败： "+err.Error())
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	writeJSON(w, 200, map[string]any{
		"source": src.Name, "url": target,
		"status": resp.StatusCode, "body": string(b),
	})
}

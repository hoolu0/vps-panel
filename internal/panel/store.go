// Package panel implements the VPS panel server: node store, agent hub,
// admin/external APIs, SSH onboarding and the embedded web UI.
package panel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// RouteSource is one pluggable outbound-route （去程） data source. The URL
// template may contain {ip}, replaced with the node's IP when queried.
type RouteSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URLTemplate string `json:"url_template"`
}

// Node is one managed VPS.
type Node struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	IP          string             `json:"ip"`
	SSHPort     int                `json:"ssh_port"`
	Token       string             `json:"token"` // agent token
	CreatedAt   time.Time          `json:"created_at"`
	Timezone    string             `json:"timezone"`
	NewSSHPort  int                `json:"new_ssh_port,omitempty"`
	Hello       *proto.Hello       `json:"hello,omitempty"`
	Status      *proto.Status      `json:"status,omitempty"`
	LastSeen    time.Time          `json:"last_seen,omitempty"`
	Probes      *proto.ProbeReport `json:"probes,omitempty"`
	Deployments []proto.Deployment `json:"deployments,omitempty"`
}

// TaskRec is the panel-side record of a task dispatched to an agent.
type TaskRec struct {
	ID        string          `json:"id"`
	NodeID    string          `json:"node_id"`
	Kind      string          `json:"kind"`
	Params    json.RawMessage `json:"params,omitempty"`
	Status    string          `json:"status"` // queued/sent/running/done/failed
	Logs      []string        `json:"logs,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// APIToken is one external API token (only its hash is stored).
type APIToken struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hash       string    `json:"-"`
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// Data is the whole persisted panel state.
type Data struct {
	AdminPasswordHash string              `json:"admin_password_hash"`
	PublicAddr        string              `json:"public_addr"`
	ProbeTargets      []proto.ProbeTarget `json:"probe_targets"`
	ProbeIntervalMin  int                 `json:"probe_interval_min"`
	RouteSources      []RouteSource       `json:"route_sources"`
	Nodes             []*Node             `json:"nodes"`
	Tasks             []*TaskRec          `json:"tasks"`
	APITokens         []*APIToken         `json:"api_tokens"`
}

// Store persists Data as one JSON file (atomic replace on save).
type Store struct {
	mu   sync.Mutex
	path string
	Data Data
}

// OpenStore loads (or initializes) the store in the data dir.
func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "store.json")}
	b, err := os.ReadFile(s.path)
	if err == nil {
		_ = json.Unmarshal(b, &s.Data)
	}
	if s.Data.ProbeTargets == nil {
		s.Data.ProbeTargets = proto.DefaultProbeTargets()
	}
	if s.Data.ProbeIntervalMin == 0 {
		s.Data.ProbeIntervalMin = 5
	}
	return s, nil
}

// Save writes the store to disk. Caller must hold s.mu (use WithLock).
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Update runs fn under the store lock and persists afterwards.
func (s *Store) Update(fn func(*Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.Data)
	return s.saveLocked()
}

// View runs fn under a read lock (the underlying mutex is exclusive; data
// volumes are tiny).
func (s *Store) View(fn func(*Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.Data)
}

// FindNode returns the node with the given id or nil. Call inside View/Update.
func FindNode(d *Data, id string) *Node {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// FindNodeByToken returns the node owning an agent token or nil.
func FindNodeByToken(d *Data, token string) *Node {
	for _, n := range d.Nodes {
		if n.Token == token {
			return n
		}
	}
	return nil
}

// FindTask returns the task with the given id or nil.
func FindTask(d *Data, id string) *TaskRec {
	for _, t := range d.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// NewID returns a random hex id with a prefix.
func NewID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// NewSecret returns a random hex secret of n bytes.
func NewSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

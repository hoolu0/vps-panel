package panel

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// agentConn is one live agent WebSocket connection.
type agentConn struct {
	nodeID string
	conn   *websocket.Conn
	mu     sync.Mutex
}

func (c *agentConn) send(env proto.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.conn.WriteJSON(env)
}

// Hub tracks live agent connections and dispatches tasks.
type Hub struct {
	store *Store
	mu    sync.Mutex
	conns map[string]*agentConn
}

func newHub(store *Store) *Hub {
	return &Hub{store: store, conns: map[string]*agentConn{}}
}

// Online reports whether the node's agent is connected.
func (h *Hub) Online(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[nodeID]
	return ok
}

func (h *Hub) register(c *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[c.nodeID]; ok {
		_ = old.conn.Close() // kick the stale connection
	}
	h.conns[c.nodeID] = c
}

func (h *Hub) unregister(c *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.conns[c.nodeID]; ok && cur == c {
		delete(h.conns, c.nodeID)
	}
}

func (h *Hub) get(nodeID string) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[nodeID]
}

// PushProbeConfig sends the current probe config to one node (if online)
// or to all online nodes when nodeID is empty.
func (h *Hub) PushProbeConfig(nodeID string) {
	var cfg proto.ProbeConfig
	h.store.View(func(d *Data) {
		cfg = proto.ProbeConfig{Targets: d.ProbeTargets, IntervalMin: d.ProbeIntervalMin}
	})
	raw, _ := json.Marshal(cfg)
	env := proto.Envelope{Type: proto.MsgProbeConfig, Data: raw}
	if nodeID != "" {
		if c := h.get(nodeID); c != nil {
			_ = c.send(env)
		}
		return
	}
	h.mu.Lock()
	conns := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.send(env)
	}
}

// Dispatch creates a task record and sends it to the agent if online;
// otherwise it stays queued and is flushed when the agent connects.
func (h *Hub) Dispatch(nodeID, kind string, params any) (*TaskRec, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	rec := &TaskRec{
		ID:        NewID("task-"),
		NodeID:    nodeID,
		Kind:      kind,
		Params:    raw,
		Status:    "queued",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	var nodeExists bool
	err := h.store.Update(func(d *Data) {
		if FindNode(d, nodeID) != nil {
			nodeExists = true
			d.Tasks = append([]*TaskRec{rec}, d.Tasks...)
			if len(d.Tasks) > 500 {
				d.Tasks = d.Tasks[:500]
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !nodeExists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}
	h.sendTask(rec)
	return rec, nil
}

func (h *Hub) sendTask(rec *TaskRec) {
	c := h.get(rec.NodeID)
	if c == nil {
		return
	}
	raw, _ := json.Marshal(proto.Task{ID: rec.ID, Kind: rec.Kind, Params: rec.Params})
	if err := c.send(proto.Envelope{Type: proto.MsgTask, Data: raw}); err != nil {
		log.Printf("hub: send task %s to node %s failed: %v", rec.ID, rec.NodeID, err)
		return
	}
	_ = h.store.Update(func(d *Data) {
		if t := FindTask(d, rec.ID); t != nil && t.Status == "queued" {
			t.Status = "sent"
			t.UpdatedAt = time.Now()
		}
	})
}

// flushQueued sends all queued tasks of a node (called when it connects).
func (h *Hub) flushQueued(nodeID string) {
	var queued []*TaskRec
	h.store.View(func(d *Data) {
		for _, t := range d.Tasks {
			if t.NodeID == nodeID && t.Status == "queued" {
				queued = append(queued, t)
			}
		}
	})
	// tasks are stored newest-first; send oldest first
	for i := len(queued) - 1; i >= 0; i-- {
		h.sendTask(queued[i])
	}
}

// --- inbound agent messages ---------------------------------------------------

func (h *Hub) onHello(nodeID string, raw json.RawMessage) {
	var hello proto.Hello
	_ = json.Unmarshal(raw, &hello)
	_ = h.store.Update(func(d *Data) {
		if n := FindNode(d, nodeID); n != nil {
			n.Hello = &hello
			n.LastSeen = time.Now()
		}
	})
	h.PushProbeConfig(nodeID)
	h.flushQueued(nodeID)
}

func (h *Hub) onStatus(nodeID string, raw json.RawMessage) {
	var st proto.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return
	}
	_ = h.store.Update(func(d *Data) {
		if n := FindNode(d, nodeID); n != nil {
			n.Status = &st
			n.LastSeen = time.Now()
		}
	})
}

func (h *Hub) onDeployments(nodeID string, raw json.RawMessage) {
	var deps []proto.Deployment
	if err := json.Unmarshal(raw, &deps); err != nil {
		return
	}
	_ = h.store.Update(func(d *Data) {
		if n := FindNode(d, nodeID); n != nil {
			// The agent does not know the panel-side public endpoint
			// overrides; carry them over from the stored records.
			for i := range deps {
				for _, old := range n.Deployments {
					if old.ID == deps[i].ID {
						if deps[i].PublicHost == "" {
							deps[i].PublicHost = old.PublicHost
						}
						if deps[i].PublicPort == 0 {
							deps[i].PublicPort = old.PublicPort
						}
					}
				}
			}
			n.Deployments = deps
			n.LastSeen = time.Now()
		}
	})
}

func (h *Hub) onProbe(nodeID string, raw json.RawMessage) {
	var rep proto.ProbeReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return
	}
	_ = h.store.Update(func(d *Data) {
		if n := FindNode(d, nodeID); n != nil {
			n.Probes = &rep
			n.LastSeen = time.Now()
		}
	})
}

func (h *Hub) onTaskLog(nodeID string, taskID string, raw json.RawMessage) {
	var tl proto.TaskLog
	_ = json.Unmarshal(raw, &tl)
	_ = h.store.Update(func(d *Data) {
		if t := FindTask(d, taskID); t != nil && t.NodeID == nodeID {
			if t.Status == "sent" || t.Status == "queued" {
				t.Status = "running"
			}
			t.Logs = append(t.Logs, tl.Line)
			if len(t.Logs) > 1000 {
				t.Logs = t.Logs[len(t.Logs)-1000:]
			}
			t.UpdatedAt = time.Now()
		}
	})
}

func (h *Hub) onTaskResult(nodeID string, taskID string, raw json.RawMessage) {
	var res proto.TaskResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return
	}
	var finished *TaskRec
	_ = h.store.Update(func(d *Data) {
		if t := FindTask(d, taskID); t != nil && t.NodeID == nodeID {
			if res.OK {
				t.Status = "done"
			} else {
				t.Status = "failed"
			}
			t.Result = res.Result
			t.Error = res.Error
			t.UpdatedAt = time.Now()
			finished = t
		}
	})
	if finished == nil {
		return
	}
	// post-task hooks
	switch finished.Kind {
	case proto.TaskDeploy:
		var dep proto.Deployment
		depOK := json.Unmarshal(res.Result, &dep) == nil && dep.ID != ""
		if depOK {
			_ = h.store.Update(func(d *Data) {
				// Apply (and consume) any public-endpoint override the
				// deploy request carried, then merge into the node.
				if ov, ok := d.PendingEndpoints[dep.ID]; ok {
					dep.PublicHost = ov.PublicHost
					dep.PublicPort = ov.PublicPort
					delete(d.PendingEndpoints, dep.ID)
				}
				if !res.OK {
					return
				}
				if n := FindNode(d, nodeID); n != nil {
					found := false
					for i, x := range n.Deployments {
						if x.ID == dep.ID {
							// keep a stored override when the incoming
							// record carries none
							if dep.PublicHost == "" {
								dep.PublicHost = x.PublicHost
							}
							if dep.PublicPort == 0 {
								dep.PublicPort = x.PublicPort
							}
							n.Deployments[i] = dep
							found = true
						}
					}
					if !found {
						n.Deployments = append(n.Deployments, dep)
					}
				}
			})
		} else {
			// Unparsable result: still consume the pending override.
			var p proto.DeployParams
			if json.Unmarshal(finished.Params, &p) == nil && p.DeploymentID != "" {
				_ = h.store.Update(func(d *Data) { delete(d.PendingEndpoints, p.DeploymentID) })
			}
		}
	case proto.TaskInit:
		if res.OK {
			h.maybeFinalizeSSH(nodeID, finished)
		}
	case proto.TaskFinalizeSSH:
		if res.OK {
			var p proto.FinalizeParams
			_ = json.Unmarshal(finished.Params, &p)
			_ = h.store.Update(func(d *Data) {
				if n := FindNode(d, nodeID); n != nil && p.NewSSHPort > 0 {
					n.SSHPort = p.NewSSHPort
					n.NewSSHPort = 0
				}
			})
		}
	}
}

// maybeFinalizeSSH verifies the new SSH port from the panel side and, when
// reachable, dispatches finalize_ssh to close the old port. If the panel
// cannot reach the port (NAT, firewall, panel behind proxy), the old port
// stays open and a note is appended to the init task log.
func (h *Hub) maybeFinalizeSSH(nodeID string, initTask *TaskRec) {
	var res struct {
		NewSSHPort      int  `json:"new_ssh_port"`
		OldSSHPort      int  `json:"old_ssh_port"`
		PendingFinalize bool `json:"pending_finalize"`
	}
	if err := json.Unmarshal(initTask.Result, &res); err != nil || !res.PendingFinalize || res.NewSSHPort == 0 {
		return
	}
	var node *Node
	h.store.View(func(d *Data) { node = FindNode(d, nodeID) })
	if node == nil {
		return
	}
	addr := net.JoinHostPort(node.IP, fmt.Sprint(res.NewSSHPort))
	go func() {
		reachable := false
		for i := 0; i < 12; i++ {
			conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err == nil {
				_ = conn.Close()
				reachable = true
				break
			}
			time.Sleep(10 * time.Second)
		}
		if !reachable {
			_ = h.store.Update(func(d *Data) {
				if t := FindTask(d, initTask.ID); t != nil {
					t.Logs = append(t.Logs, "面板侧无法直连新 SSH 端口验证，旧端口保持开启（可在节点详情手动收尾）")
					t.UpdatedAt = time.Now()
				}
			})
			return
		}
		_, err := h.Dispatch(nodeID, proto.TaskFinalizeSSH, proto.FinalizeParams{
			NewSSHPort: res.NewSSHPort,
			OldSSHPort: node.SSHPort,
		})
		if err != nil {
			log.Printf("hub: dispatch finalize_ssh for node %s: %v", nodeID, err)
		}
	}()
}

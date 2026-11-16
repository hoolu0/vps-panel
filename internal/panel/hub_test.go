package panel

import (
	"testing"
	"time"
)

// Online() grace semantics: a live connection is online; a disconnect
// inside the grace window still counts as online; past the window (or
// never connected) is offline.
func TestOnlineGrace(t *testing.T) {
	h := newHub(nil)

	// never connected
	if h.Online("n1") {
		t.Fatal("never-connected node must be offline")
	}

	// connected
	c := &agentConn{nodeID: "n1"}
	h.register(c)
	if !h.Online("n1") {
		t.Fatal("connected node must be online")
	}

	// just disconnected: inside the grace window
	h.unregister(c)
	if !h.Online("n1") {
		t.Fatal("node inside offline grace must still count as online")
	}

	// disconnect stamped past the grace window
	h.mu.Lock()
	h.downSince["n1"] = time.Now().Add(-offlineGrace - time.Second)
	h.mu.Unlock()
	if h.Online("n1") {
		t.Fatal("node past offline grace must be offline")
	}

	// reconnecting clears the stale downSince record
	c2 := &agentConn{nodeID: "n1"}
	h.register(c2)
	if !h.Online("n1") {
		t.Fatal("reconnected node must be online")
	}
	h.mu.Lock()
	_, hasDown := h.downSince["n1"]
	h.mu.Unlock()
	if hasDown {
		t.Fatal("register must clear the downSince record")
	}

	// a stale unregister (old conn racing a newer register) must not
	// record a disconnect
	h.unregister(c)
	if !h.Online("n1") {
		t.Fatal("stale unregister must not drop the current connection")
	}
	h.mu.Lock()
	_, hasDown = h.downSince["n1"]
	h.mu.Unlock()
	if hasDown {
		t.Fatal("stale unregister must not record downSince")
	}

	// forget removes the grace record (node deleted)
	h.unregister(c2)
	h.forget("n1")
	if h.Online("n1") {
		t.Fatal("forgotten node must be offline even inside the grace window")
	}
}

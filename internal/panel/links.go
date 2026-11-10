package panel

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/hoolu0/vps-panel/internal/proto"
)

// ComposeLink builds the client share link for a deployment on a node.
func ComposeLink(n *Node, d proto.Deployment) string {
	remark := url.QueryEscape(n.Name + "-" + d.Module)
	switch d.Module {
	case proto.ModuleVLESSReality:
		q := url.Values{}
		q.Set("security", "reality")
		q.Set("encryption", "none")
		q.Set("pbk", d.PublicKey)
		q.Set("fp", "chrome")
		q.Set("type", "tcp")
		q.Set("flow", "xtls-rprx-vision")
		q.Set("sni", d.ServerName)
		if d.ShortID != "" {
			q.Set("sid", d.ShortID)
		}
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", d.UUID, n.IP, d.Port, q.Encode(), remark)
	case proto.ModuleSS2022:
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(d.Method + ":" + d.Password))
		return fmt.Sprintf("ss://%s@%s:%d#%s", userinfo, n.IP, d.Port, remark)
	}
	return ""
}

// sanitizeFileName makes a string safe for use in a QR download name.
func sanitizeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/hoolu0/vps-panel/internal/panel"
	"github.com/hoolu0/vps-panel/internal/proto"
)

func randomPassword(n int) string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	for i := range out {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		out[i] = chars[v.Int64()]
	}
	return string(out)
}

func main() {
	var (
		addr    = flag.String("addr", ":8080", "listen address")
		dataDir = flag.String("data", "./data", "data directory (store, kept across updates)")
		version = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *version {
		fmt.Println("vps-panel", proto.Version)
		return
	}

	store, err := panel.OpenStore(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	srv := panel.NewServer(store)

	if !srv.HasAdminPassword() {
		pw := os.Getenv("PANEL_ADMIN_PASSWORD")
		generated := false
		if pw == "" {
			pw = randomPassword(16)
			generated = true
		}
		if err := srv.SetAdminPassword(pw); err != nil {
			log.Fatalf("set admin password: %v", err)
		}
		if generated {
			log.Printf("==================================================")
			log.Printf("  首次启动，已生成管理员密码： %s", pw)
			log.Printf("  请登录后在设置中修改。")
			log.Printf("==================================================")
		} else {
			log.Printf("管理员密码已从 PANEL_ADMIN_PASSWORD 初始化")
		}
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("vps-panel %s listening on %s (data: %s)", proto.Version, *addr, *dataDir)
	log.Fatal(httpSrv.ListenAndServe())
}

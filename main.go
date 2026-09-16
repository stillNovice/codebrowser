// Command codebrowse is a localhost code browser: file tree + editor,
// git diff view, structural outline, and an agent chat panel.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"strings"

	"codebrowse/internal/agent"
	"codebrowse/internal/comments"
	"codebrowse/internal/gitx"
	"codebrowse/internal/piagent"
	"codebrowse/internal/server"
	"codebrowse/internal/symbols"
	"codebrowse/internal/xref"
)

// BuildVersion is stamped at build time via -ldflags "-X main.BuildVersion=...".
var BuildVersion = "dev"

func main() {
	repo := flag.String("repo", ".", "repository root to browse (-r)")
	host := flag.String("host", "127.0.0.1", "listen host (default localhost only)")
	port := flag.Int("port", 4004, "listen port (-p); if busy, the next 20 ports are probed")
	session := flag.String("session", "", "resume pi session (path or id); the terminal pi using it must be stopped first")
	attach := flag.Bool("attach", true, "resume the most recent pi session for this repo (-no-attach for a fresh session)")
	noAttach := flag.Bool("no-attach", false, "start a fresh pi session instead of resuming")
	open := flag.String("open", "", "file to open on load (repo-relative or absolute, must be inside root)")
	noChat := flag.Bool("no-chat", false, "disable the agent chat panel (comments flow to the terminal pi via file)")
	mode := flag.String("mode", "", "launch mode (-m): review (PR review) or build (feature work); empty = default")
	// Shorthands — declared as real flags, folded into the canonical
	// ones after Parse (last-one-wins: explicit long form beats short).
	pShort := flag.String("p", "", "shorthand for -port")
	mShort := flag.String("m", "", "shorthand for -mode")
	rShort := flag.String("r", "", "shorthand for -repo")
	chatDeprecated := flag.Bool("chat", false, "deprecated: chat is on by default; use -no-chat to disable")
	flag.Parse()
	if *pShort != "" {
		if v, err := strconv.Atoi(*pShort); err == nil {
			*port = v
		} else {
			log.Fatalf("-p: invalid port %q", *pShort)
		}
	}
	if *mShort != "" {
		*mode = *mShort
	}
	if *rShort != "" {
		*repo = *rShort
	}

	root, err := filepath.Abs(*repo)
	if err != nil {
		log.Fatal(err)
	}

	sess := *session
	if *noAttach {
		*attach = false
	}
	if *attach && sess == "" {
		sess, err = piagent.LatestSession(root)
		if err != nil {
			log.Printf("warning: -attach: %v (starting fresh session)", err)
			sess = ""
		} else {
			log.Printf("agent session attached: %s", strings.TrimSuffix(filepath.Base(sess), ".jsonl"))
		}
	}

	// Model catalog (for the dropdown) still comes from pi's config
	// or OPENAI_* env — used by both backends.
	ag, err := agent.Load()
	if err != nil {
		log.Printf("warning: %v (model list unavailable)", err)
		ag = nil
	}

	// Chat backend is on by default (embedded pi session). -no-chat turns
	// the browser back into a pure review surface: comments flow to the
	// terminal pi via the repo JSONL file. (-chat itself is deprecated
	// and ignored — it was the old opt-in switch.)
	var pi *piagent.Session
	if *chatDeprecated {
		log.Printf("note: -chat is deprecated (chat is on by default); ignored")
	}
	if *noChat {
		goto noChat
	}
	{
		model := ""
		if ag != nil {
			model = strings.TrimPrefix(ag.Default(), "env/")
		}
		if p, err := piagent.Start(root, model, sess); err != nil {
			log.Printf("warning: pi backend unavailable: %v", err)
		} else {
			pi = p
			log.Printf("agent backend: pi (tools enabled, session persists)")
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
			go func() { <-sig; pi.Close(); os.Exit(0) }()
		}
	}
noChat:

	openRel := ""
	if *open != "" {
		abs := *open
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		abs = filepath.Clean(abs)
		if abs == root || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			log.Printf("warning: -open path outside repo root, ignoring")
		} else if info, err := os.Stat(abs); err != nil || info.IsDir() {
			log.Printf("warning: -open %s not found, ignoring", *open)
		} else {
			openRel, _ = filepath.Rel(root, abs)
		}
	}

	server.Version = BuildVersion
	xr := xref.New(root)
	go func() {
		if err := xr.Build(); err != nil {
			log.Printf("warning: xref index: %v", err)
		}
	}()

	srv := &server.Server{
		Root:        root,
		Git:         gitx.New(root),
		Symbols:     symbols.New(),
		Xref:        xr,
		Agent:       ag,
		Pi:          pi,
		Session:     sess != "",
		SessionFile: sess,
		OpenFile:    filepath.ToSlash(openRel),
		Mode:        *mode,
		Comments:    comments.Open(root),
	}
	go srv.WatchTree()

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Port busy (another repo's codebrowse?) — probe upward.
		for i := 1; i <= 20 && err != nil; i++ {
			addr = net.JoinHostPort(*host, strconv.Itoa(*port+i))
			ln, err = net.Listen("tcp", addr)
		}
		if err != nil {
			log.Fatalf("no free port near %d: %v", *port, err)
		}
	}
	log.Printf("codebrowse serving %s on http://%s", filepath.Base(root), addr)
	log.Fatal(http.Serve(ln, srv.Routes()))
}

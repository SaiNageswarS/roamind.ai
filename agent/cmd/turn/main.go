// Command turn runs one agent turn from the terminal and prints its packets.
//
//	go run ./cmd/turn -user dev "hello"
//	go run ./cmd/turn -user dev -session <id> "what did I just say?"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"path/filepath"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/memory"
)

func main() {
	user := flag.String("user", "dev", "user id")
	session := flag.String("session", "", "claude session id to resume")
	dataDir := flag.String("data", "./data", "data directory")
	model := flag.String("model", os.Getenv("ROAMIND_MODEL"), "claude model")
	email := flag.String("email", "", "the user's email address")
	project := flag.String("memory-project", "", "Basic Memory project (default bm-u-<user>-1); \"off\" disables memory")
	flag.Parse()

	cfg := agent.Config{DataDir: *dataDir, Model: *model}
	in := protocol.TurnInput{UserID: *user, UserName: *user, UserEmail: *email, SessionID: *session, Text: strings.Join(flag.Args(), " ")}
	if *project != "off" {
		mem, err := memory.New(filepath.Join(*dataDir, "memory"), os.Getenv("ROAMIND_BM_BIN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		mem.Search = memory.DefaultSearch
		cfg.Memory, in.MemoryProject = mem, *project
		if in.MemoryProject == "" {
			in.MemoryProject = memory.ProjectBase(*user) + "-1"
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	enc := json.NewEncoder(os.Stdout)
	out, err := agent.RunTurn(ctx, cfg, in, func(p protocol.Packet) { _ = enc.Encode(p) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\nsession=%s cost=$%.4f reset=%v\n", out.SessionID, out.CostUSD, out.SessionReset)
}

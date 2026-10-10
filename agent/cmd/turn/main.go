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

	"roamind.ai/agent"
	"roamind.ai/protocol"
)

func main() {
	user := flag.String("user", "dev", "user id")
	session := flag.String("session", "", "claude session id to resume")
	dataDir := flag.String("data", "./data", "data directory")
	model := flag.String("model", os.Getenv("ROAMIND_MODEL"), "claude model")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	enc := json.NewEncoder(os.Stdout)
	out, err := agent.RunTurn(ctx, agent.Config{DataDir: *dataDir, Model: *model},
		protocol.TurnInput{UserID: *user, SessionID: *session, Text: strings.Join(flag.Args(), " ")},
		"terminal", func(p protocol.Packet) { _ = enc.Encode(p) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\nsession=%s cost=$%.4f reset=%v\n", out.SessionID, out.CostUSD, out.SessionReset)
}

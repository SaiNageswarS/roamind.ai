package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	gateway := flag.String("gateway", envOr("GATEWAY_ADDR", "localhost:50051"), "Gateway gRPC address")
	userID := flag.String("user", envOr("CLI_USER_ID", "default"), "User ID")
	timeout := flag.Duration("timeout", 90*time.Second, "Overall request timeout")
	flag.Parse()

	conn, err := grpc.NewClient(
		*gateway,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("dial %s: %v", *gateway, err)
	}
	defer conn.Close()

	client := pb.NewAssistantCLIClient(conn)
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("Query > ")

		line, err := reader.ReadString('\n')
		if err == io.EOF {
			fmt.Println()
			return
		}
		if err != nil {
			log.Printf("read input: %v", err)
			continue
		}

		message := strings.TrimSpace(line)
		if message == "" {
			continue
		}

		if err := queryOnce(client, *userID, *timeout, message); err != nil {
			log.Printf("query failed: %v", err)
		}
	}
}

func queryOnce(client pb.AssistantCLIClient, userID string, timeout time.Duration, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req := &pb.QueryRequest{
		Id:         uuid.NewString(),
		UserId:     userID,
		Text:       message,
		ReceivedAt: timestamppb.Now(),
	}

	stream, err := client.Query(ctx, req)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		printResponse(resp)
	}
}

func printResponse(r *pb.QueryResponse) {
	intent := r.GetIntent()
	if intent == "" {
		intent = "reply"
	}
	fmt.Printf("[%s] %s\n", intent, r.GetReply())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

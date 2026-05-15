package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
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
	message := flag.String("msg", "", "Message to send (required)")
	timeout := flag.Duration("timeout", 90*time.Second, "Overall request timeout")
	flag.Parse()

	if *message == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\nerror: -msg is required")
		os.Exit(2)
	}

	conn, err := grpc.NewClient(
		*gateway,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("dial %s: %v", *gateway, err)
	}
	defer conn.Close()

	client := pb.NewAssistantCLIClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	req := &pb.QueryRequest{
		Id:         uuid.NewString(),
		UserId:     *userID,
		Text:       *message,
		ReceivedAt: timestamppb.Now(),
	}

	stream, err := client.Query(ctx, req)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			log.Fatalf("recv: %v", err)
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

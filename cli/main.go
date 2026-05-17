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
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	gateway := flag.String("gateway", envOr("GATEWAY_ADDR", "localhost:50051"), "Gateway gRPC address")
	token := flag.String("token", os.Getenv("CLI_JWT_TOKEN"), "JWT bearer token (or env CLI_JWT_TOKEN)")
	timeout := flag.Duration("timeout", 90*time.Second, "Overall request timeout")
	flag.Parse()

	if *token == "" {
		log.Fatalf("jwt token is required: pass -token or set CLI_JWT_TOKEN")
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
	input := newInputReader()

	for {
		line, err := input.readLine("Query > ")
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

		if err := queryOnce(client, *token, *timeout, message); err != nil {
			log.Printf("query failed: %v", err)
		}
	}
}

type inputReader struct {
	fallback *bufio.Reader
	history  []string
	useTTY   bool
}

func newInputReader() *inputReader {
	return &inputReader{
		fallback: bufio.NewReader(os.Stdin),
		useTTY:   term.IsTerminal(int(os.Stdin.Fd())),
	}
}

func (r *inputReader) readLine(prompt string) (string, error) {
	if !r.useTTY {
		fmt.Print(prompt)
		line, err := r.fallback.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	line, err := readLineWithHistory(prompt, r.history)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(line) != "" {
		r.history = append(r.history, line)
	}
	return line, nil
}

func readLineWithHistory(prompt string, history []string) (string, error) {
	fd := int(os.Stdin.Fd())
	originalState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Print(prompt)
		reader := bufio.NewReader(os.Stdin)
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return "", readErr
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	defer term.Restore(fd, originalState)

	f := os.NewFile(uintptr(fd), "/dev/stdin")
	if f == nil {
		return "", fmt.Errorf("stdin unavailable")
	}

	fmt.Print(prompt)
	buf := make([]byte, 0, 256)
	historyIdx := len(history)
	draft := make([]byte, 0, 256)

	redraw := func() {
		fmt.Printf("\r%s%s\x1b[K", prompt, string(buf))
	}

	one := make([]byte, 1)
	for {
		if _, err := f.Read(one); err != nil {
			return "", err
		}

		switch one[0] {
		case '\r', '\n':
			fmt.Print("\r\n")
			return string(buf), nil
		case 3: // Ctrl+C
			fmt.Print("\r\n")
			return "", nil
		case 4: // Ctrl+D
			if len(buf) == 0 {
				return "", io.EOF
			}
		case 8, 127: // Backspace / DEL
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				redraw()
			}
		case 27: // Escape sequence
			seq := make([]byte, 2)
			if _, err := io.ReadFull(f, seq); err != nil {
				continue
			}
			if seq[0] != '[' {
				continue
			}

			switch seq[1] {
			case 'A': // Up
				if len(history) == 0 || historyIdx == 0 {
					continue
				}
				if historyIdx == len(history) {
					draft = append(draft[:0], buf...)
				}
				historyIdx--
				buf = append(buf[:0], history[historyIdx]...)
				redraw()
			case 'B': // Down
				if len(history) == 0 || historyIdx >= len(history) {
					continue
				}
				historyIdx++
				if historyIdx == len(history) {
					buf = append(buf[:0], draft...)
				} else {
					buf = append(buf[:0], history[historyIdx]...)
				}
				redraw()
			}
		default:
			if one[0] >= 32 && one[0] != 127 {
				buf = append(buf, one[0])
				redraw()
			}
		}
	}
}

func queryOnce(client pb.AssistantCLIClient, token string, timeout time.Duration, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "bearer "+token)

	req := &pb.QueryRequest{
		Id:         uuid.NewString(),
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

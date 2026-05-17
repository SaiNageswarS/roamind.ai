package services

import (
	"context"
	"sync"
	"time"

	"github.com/SaiNageswarS/go-api-boot/auth"
	"github.com/SaiNageswarS/go-api-boot/logger"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultQueryTimeout is how long a CLI Query stream will wait for an agent reply
// before returning DeadlineExceeded.
const defaultQueryTimeout = 60 * time.Second

// pendingReply correlates a CLI gRPC stream with an inbound tasks.out envelope.
type pendingReply struct {
	ch        chan *pb.TaskOut
	createdAt time.Time
}

// CliService implements the AssistantCLI gRPC service.
//
// Flow:
//  1. Client calls Query(req) — opens a server-side stream.
//  2. Service generates an internal ID, registers a pending channel, then XADDs
//     a TaskIn envelope on tasks.in with channel="cli".
//  3. The background egress consumer reads tasks.out and dispatches replies
//     to pending channels by InReplyTo.
//  4. Query reads from its channel and Send()s a QueryResponse, then closes.
type CliService struct {
	pb.UnimplementedAssistantCLIServer

	rdb     *redis.Client
	pending sync.Map // map[string]*pendingReply
	timeout time.Duration
}

// NewCliService is the DI factory consumed by go-api-boot's RegisterService.
func NewCliService(rdb *redis.Client) *CliService {
	svc := &CliService{
		rdb:     rdb,
		timeout: defaultQueryTimeout,
	}

	// Background egress consumer runs for the life of the process.
	go svc.runEgressConsumer(context.Background())

	return svc
}

// Query implements AssistantCLI.Query (server-side streaming).
func (s *CliService) Query(req *pb.QueryRequest, stream pb.AssistantCLI_QueryServer) error {
	if req == nil || req.GetText() == "" {
		return status.Error(codes.InvalidArgument, "text is required")
	}

	ctx := stream.Context()
	userID, _ := auth.GetUserIdAndTenant(ctx)

	internalID := uuid.NewString()
	traceID := req.GetId()
	if traceID == "" {
		traceID = internalID
	}

	taskIn := &pb.TaskIn{
		Id:           internalID,
		TraceId:      traceID,
		UserId:       userID,
		Channel:      "cli",
		ChannelMsgId: req.GetId(),
		Text:         req.GetText(),
		ReceivedAt:   timestamppb.Now(),
	}

	replyCh := make(chan *pb.TaskOut, 1)
	s.pending.Store(internalID, &pendingReply{ch: replyCh, createdAt: time.Now()})
	defer s.pending.Delete(internalID)

	if _, err := XAddTaskIn(ctx, s.rdb, taskIn); err != nil {
		logger.Error("xadd tasks.in failed", zap.Error(err), zap.String("id", internalID))
		return status.Errorf(codes.Internal, "enqueue failed: %v", err)
	}

	logger.Info("CLI Query enqueued",
		zap.String("id", internalID),
		zap.String("user_id", userID))

	select {
	case taskOut := <-replyCh:
		resp := &pb.QueryResponse{
			Id:        taskOut.GetInReplyTo(),
			Reply:     taskOut.GetPayload(),
			Intent:    taskOut.GetIntent(),
			CreatedAt: taskOut.GetCreatedAt(),
		}
		if err := stream.Send(resp); err != nil {
			return status.Errorf(codes.Internal, "send: %v", err)
		}
		return nil

	case <-time.After(s.timeout):
		return status.Error(codes.DeadlineExceeded, "no agent reply within timeout")

	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

// runEgressConsumer reads tasks.out continuously and routes CLI-channel
// envelopes back to waiting Query streams.
func (s *CliService) runEgressConsumer(ctx context.Context) {
	if err := EnsureGroup(ctx, s.rdb, StreamTasksOut, GroupGatewayEgress); err != nil {
		logger.Error("ensure egress group failed", zap.Error(err))
		return
	}
	logger.Info("Gateway egress consumer started",
		zap.String("stream", StreamTasksOut),
		zap.String("group", GroupGatewayEgress))

	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := XReadTasksOut(ctx, s.rdb, 16, 2*time.Second)
		if err != nil {
			logger.Error("xread tasks.out failed", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		for _, msg := range msgs {
			taskOut, err := ParseTaskOut(msg)
			if err != nil {
				logger.Error("parse tasks.out failed", zap.Error(err), zap.String("msg_id", msg.ID))
				XAckTasksOut(ctx, s.rdb, msg.ID)
				continue
			}

			// Only handle CLI channel; other channels are owned by other adapters.
			if taskOut.GetChannel() != "cli" {
				XAckTasksOut(ctx, s.rdb, msg.ID)
				continue
			}

			inReplyTo := taskOut.GetInReplyTo()
			if v, ok := s.pending.Load(inReplyTo); ok {
				p := v.(*pendingReply)
				select {
				case p.ch <- taskOut:
					logger.Info("CLI reply delivered", zap.String("in_reply_to", inReplyTo))
				default:
					logger.Info("CLI reply channel full; dropping",
						zap.String("in_reply_to", inReplyTo))
				}
			} else {
				logger.Info("CLI reply has no pending request",
					zap.String("in_reply_to", inReplyTo))
			}

			XAckTasksOut(ctx, s.rdb, msg.ID)
		}
	}
}

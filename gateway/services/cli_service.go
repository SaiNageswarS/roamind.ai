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

// defaultQueryTimeout is how long a CLI Query stream will wait for an agent
// reply before returning DeadlineExceeded.
const defaultQueryTimeout = 60 * time.Second

// CliService implements the AssistantCLI gRPC service.
//
// Flow:
//  1. Client calls Query(req) — opens a server-side stream.
//  2. Service generates an internal ID, registers a pending channel, then
//     XADDs a TaskIn envelope on tasks.in with channel="cli".
//  3. The shared EgressDispatcher invokes handleEgress for every "cli"
//     taskOut and routes the reply by in_reply_to.
//  4. Query reads from its channel and Send()s a QueryResponse, then closes.
type CliService struct {
	pb.UnimplementedAssistantCLIServer

	rdb     *redis.Client
	habit   *HabitService
	pending sync.Map // map[string]*pendingReply
	timeout time.Duration
}

// NewCliService is the DI factory consumed by go-api-boot's RegisterService.
// It registers itself as the "cli" egress handler on the shared dispatcher.
func NewCliService(rdb *redis.Client, dispatcher *EgressDispatcher, habit *HabitService) *CliService {
	svc := &CliService{
		rdb:     rdb,
		habit:   habit,
		timeout: defaultQueryTimeout,
	}
	dispatcher.Register("cli", svc.handleEgress)
	return svc
}

// Query implements AssistantCLI.Query (server-side streaming).
func (s *CliService) Query(req *pb.QueryRequest, stream pb.AssistantCLI_QueryServer) error {
	if req == nil || req.GetText() == "" {
		return status.Error(codes.InvalidArgument, "text is required")
	}

	ctx := stream.Context()
	userID, _ := auth.GetUserIdAndTenant(ctx)

	// Short-circuit gateway-owned /habit_* commands before paying for LLM.
	if reply, handled, herr := s.habit.ParseAndExecute(ctx, userID, req.GetText()); handled {
		if herr != nil {
			logger.Error("habit command failed", zap.Error(herr), zap.String("user_id", userID))
		}
		resp := &pb.QueryResponse{
			Id:        req.GetId(),
			Reply:     reply,
			Intent:    "reply",
			CreatedAt: timestamppb.Now(),
		}
		if err := stream.Send(resp); err != nil {
			return status.Errorf(codes.Internal, "send: %v", err)
		}
		return nil
	}

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

// --- internals ----------------------------------------------------------

// pendingReply correlates a CLI gRPC stream with an inbound tasks.out
// envelope.
type pendingReply struct {
	ch        chan *pb.TaskOut
	createdAt time.Time
}

// handleEgress is registered with the EgressDispatcher for the "cli"
// channel. It routes a TaskOut to the waiting Query goroutine via its
// pending channel.
func (s *CliService) handleEgress(_ context.Context, taskOut *pb.TaskOut) {
	inReplyTo := taskOut.GetInReplyTo()
	v, ok := s.pending.Load(inReplyTo)
	if !ok {
		logger.Info("CLI reply has no pending request",
			zap.String("in_reply_to", inReplyTo))
		return
	}
	p := v.(*pendingReply)
	select {
	case p.ch <- taskOut:
		logger.Info("CLI reply delivered", zap.String("in_reply_to", inReplyTo))
	default:
		logger.Info("CLI reply channel full; dropping",
			zap.String("in_reply_to", inReplyTo))
	}
}

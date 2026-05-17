package services

import (
	"context"
	"sync"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// EgressHandler processes a single TaskOut envelope delivered to a channel.
// Implementations should return quickly; long work must be dispatched to
// another goroutine.
type EgressHandler func(ctx context.Context, task *pb.TaskOut)

// EgressDispatcher consumes tasks.out and routes each envelope to the
// per-channel handler registered for taskOut.channel.
//
// Exactly one dispatcher should run per gateway process: it is the single
// consumer in the GroupGatewayEgress consumer group, so adding more readers
// would split work and silently drop messages.
type EgressDispatcher struct {
	rdb      *redis.Client
	mu       sync.RWMutex
	handlers map[string]EgressHandler
	once     sync.Once
}

// NewEgressDispatcher constructs an idle dispatcher. Call Start to begin
// consuming from Redis.
func NewEgressDispatcher(rdb *redis.Client) *EgressDispatcher {
	return &EgressDispatcher{
		rdb:      rdb,
		handlers: make(map[string]EgressHandler),
	}
}

// Register binds a handler to a channel name (e.g. "cli", "telegram").
// A second call for the same channel overwrites the previous handler.
func (d *EgressDispatcher) Register(channel string, h EgressHandler) {
	d.mu.Lock()
	d.handlers[channel] = h
	d.mu.Unlock()
	logger.Info("egress handler registered", zap.String("channel", channel))
}

// Start launches the consumer loop. Idempotent — subsequent calls are no-ops.
func (d *EgressDispatcher) Start(ctx context.Context) {
	d.once.Do(func() { go d.run(ctx) })
}

// --- internals ----------------------------------------------------------

func (d *EgressDispatcher) run(ctx context.Context) {
	if err := EnsureGroup(ctx, d.rdb, StreamTasksOut, GroupGatewayEgress); err != nil {
		logger.Error("ensure egress group failed", zap.Error(err))
		return
	}
	logger.Info("Gateway egress dispatcher started",
		zap.String("stream", StreamTasksOut),
		zap.String("group", GroupGatewayEgress))

	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := XReadTasksOut(ctx, d.rdb, 16, 2*time.Second)
		if err != nil {
			logger.Error("xread tasks.out failed", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		for _, msg := range msgs {
			taskOut, err := ParseTaskOut(msg)
			if err != nil {
				logger.Error("parse tasks.out failed",
					zap.Error(err), zap.String("msg_id", msg.ID))
				XAckTasksOut(ctx, d.rdb, msg.ID)
				continue
			}

			channel := taskOut.GetChannel()
			d.mu.RLock()
			h, ok := d.handlers[channel]
			d.mu.RUnlock()

			if ok {
				h(ctx, taskOut)
			} else {
				logger.Info("no handler for channel; dropping",
					zap.String("channel", channel),
					zap.String("in_reply_to", taskOut.GetInReplyTo()))
			}

			XAckTasksOut(ctx, d.rdb, msg.ID)
		}
	}
}

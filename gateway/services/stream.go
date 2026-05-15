package services

import (
	"context"
	"fmt"
	"time"

	"github.com/SaiNageswarS/go-api-boot/logger"
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

// Redis stream names and consumer groups.
const (
	StreamTasksIn  = "tasks.in"
	StreamTasksOut = "tasks.out"
	StreamTasksDLQ = "tasks.dlq"

	GroupGatewayEgress = "gateway-egress"
	ConsumerGateway    = "gateway-1"

	streamPayloadField = "payload"
)

// EnsureGroup creates a consumer group on a stream if it does not yet exist.
// Stream is created (MKSTREAM) if missing.
func EnsureGroup(ctx context.Context, rdb *redis.Client, stream, group string) error {
	err := rdb.XGroupCreateMkStream(ctx, stream, group, "$").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("xgroup create %s/%s: %w", stream, group, err)
	}
	return nil
}

// XAddTaskIn marshals a TaskIn envelope and pushes it onto tasks.in.
func XAddTaskIn(ctx context.Context, rdb *redis.Client, task *pb.TaskIn) (string, error) {
	data, err := protojson.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("marshal TaskIn: %w", err)
	}
	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamTasksIn,
		Values: map[string]any{streamPayloadField: string(data)},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("xadd tasks.in: %w", err)
	}
	return id, nil
}

// XReadTasksOut blocks (up to block) reading new messages from tasks.out for the
// gateway egress consumer group.
func XReadTasksOut(ctx context.Context, rdb *redis.Client, count int64, block time.Duration) ([]redis.XMessage, error) {
	streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    GroupGatewayEgress,
		Consumer: ConsumerGateway,
		Streams:  []string{StreamTasksOut, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	if len(streams) == 0 {
		return nil, nil
	}
	return streams[0].Messages, nil
}

// XAckTasksOut acknowledges processed messages.
func XAckTasksOut(ctx context.Context, rdb *redis.Client, ids ...string) {
	if len(ids) == 0 {
		return
	}
	if err := rdb.XAck(ctx, StreamTasksOut, GroupGatewayEgress, ids...).Err(); err != nil {
		logger.Error("xack tasks.out failed", zap.Error(err), zap.Strings("ids", ids))
	}
}

// ParseTaskOut unmarshals the payload field of an XMessage into a TaskOut.
func ParseTaskOut(msg redis.XMessage) (*pb.TaskOut, error) {
	raw, ok := msg.Values[streamPayloadField].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s field in message %s", streamPayloadField, msg.ID)
	}
	var task pb.TaskOut
	if err := protojson.Unmarshal([]byte(raw), &task); err != nil {
		return nil, fmt.Errorf("unmarshal TaskOut: %w", err)
	}
	return &task, nil
}

package handlers

import (
	"context"

	"github.com/SaiNageswarS/roamind.ai/memory/db"
)

func (h *Handlers) HandleInitializeMemory(ctx context.Context) error {
	return db.InitMemory(ctx, h.Mongo)
}

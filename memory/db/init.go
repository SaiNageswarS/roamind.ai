package db

import (
	"context"

	"github.com/SaiNageswarS/go-api-boot/odm"
)

func InitMemory(ctx context.Context, mongo odm.MongoClient) error {
	err := odm.EnsureIndexes[UserModel](ctx, mongo, tenant)
	if err != nil {
		return err
	}

	err = odm.EnsureIndexes[ProfileCardModel](ctx, mongo, tenant)
	if err != nil {
		return err
	}

	err = odm.EnsureIndexes[ProfileCardEmbeddingModel](ctx, mongo, tenant)
	if err != nil {
		return err
	}

	return nil
}

const tenant = "roamind"

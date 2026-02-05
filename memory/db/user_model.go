package db

import (
	"github.com/SaiNageswarS/go-api-boot/odm"
)

type UserModel struct {
	UserID      string `bson:"_id"`
	Email       string `bson:"email"`
	PhoneNumber string `bson:"phone_number"`
	Name        string `bson:"name"`
}

func (m UserModel) Id() string {
	if m.UserID == "" {
		m.UserID, _ = odm.HashedKey(m.Email)
	}

	return m.UserID
}

func (m UserModel) CollectionName() string {
	return "users"
}

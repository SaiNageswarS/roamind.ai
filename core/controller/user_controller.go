package controller

import "github.com/SaiNageswarS/go-api-boot/odm"

type UserController struct {
	mongo odm.MongoClient
}

func ProvideUserController(mongo odm.MongoClient) *UserController {
	return &UserController{mongo: mongo}
}

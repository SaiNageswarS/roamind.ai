package services

import (
	pb "github.com/SaiNageswarS/roamind.ai/proto/generated"
)

type CliService struct {
	pb.UnimplementedAssistantCLIServer
}

package main

import (
	"time"

	"github.com/SaiNageswarS/roamind.ai/workflows/activities"
	"go.temporal.io/sdk/workflow"
)

func SaveProfileCardWorkflow(ctx workflow.Context, input SaveProfileCardInput) error {
	activityOpts := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute * 20,
	}

	ctx = workflow.WithActivityOptions(ctx, activityOpts)

	var profileCardId string
	err := workflow.ExecuteActivity(
		ctx,
		(*activities.Activities).SaveProfileCard,
		input.UserId,
		input.Key,
		input.Title,
		input.Aliases,
		input.ContentMdFilePath,
	).Get(ctx, &profileCardId)

	if err != nil {
		return err
	}

	// Next segment the profile card
	var segmentedProfileCardId string
	err = workflow.ExecuteActivity(
		ctx,
		(*activities.Activities).SegmentProfileCard,
		profileCardId,
	).Get(ctx, &segmentedProfileCardId)

	if err != nil {
		return err
	}

	// Now embed the profile card
	err = workflow.ExecuteActivity(
		ctx,
		(*activities.Activities).EmbedProfileCard,
		segmentedProfileCardId,
	).Get(ctx, nil)

	return err
}

type SaveProfileCardInput struct {
	UserId            string
	Key               string
	Title             string
	Aliases           []string
	ContentMdFilePath string
}

package main

import (
	"os"

	"github.com/SaiNageswarS/go-api-boot/dotenv"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/roamind.ai/cli/handlers"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var rootCmd = &cobra.Command{
	Use:   "roamind-cli",
	Short: "RoamindAI CLI tool",
	Long:  "A CLI tool for interacting with RoamindAI services",
}

var cmdHandlers = &handlers.Handlers{}

var getTokenCmd = &cobra.Command{
	Use:   "getToken [email]",
	Short: "Get token for a user",
	Long:  "Retrieve authentication token for the specified user ID",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := cmdHandlers.HandleGetToken(args[0]); err != nil {
			logger.Error("Error getting token", zap.Error(err))
			os.Exit(1)
		}
	},
}

var createUserCmd = &cobra.Command{
	Use:   "createUser -e <email> -n <name> -p <phoneNumber>",
	Short: "Create a new user",
	Long:  "Create a new user with email, name, and phone number",
	Run: func(cmd *cobra.Command, args []string) {
		email, _ := cmd.Flags().GetString("email")
		name, _ := cmd.Flags().GetString("name")
		phoneNumber, _ := cmd.Flags().GetString("phoneNumber")

		if err := cmdHandlers.HandleCreateUser(cmd.Context(), email, name, phoneNumber); err != nil {
			logger.Error("Error creating user", zap.Error(err))
			os.Exit(1)
		}
	},
}

func init() {
	dotenv.LoadEnv()
	cmdHandlers.Mongo = odm.ProvideMongoClient()

	// Add flags to createUser command
	createUserCmd.Flags().StringP("email", "e", "", "Email address of the user (required)")
	createUserCmd.Flags().StringP("name", "n", "", "Full name of the user (required)")
	createUserCmd.Flags().StringP("phoneNumber", "p", "", "Phone number of the user (required)")

	// Mark flags as required
	createUserCmd.MarkFlagRequired("email")
	createUserCmd.MarkFlagRequired("name")
	createUserCmd.MarkFlagRequired("phoneNumber")

	// Add commands to root command
	rootCmd.AddCommand(getTokenCmd)
	rootCmd.AddCommand(createUserCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		logger.Error("Error executing command", zap.Error(err))
		os.Exit(1)
	}
}

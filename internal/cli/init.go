package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/config"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the mealcli data directory and config",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Resolve()
			if err != nil {
				return err
			}

			existed, err := config.ConfigFileExists()
			if err != nil {
				return err
			}
			if !existed {
				if err := config.WriteDataRoot(cfg.DataRoot); err != nil {
					return fmt.Errorf("writing config file: %w", err)
				}
			}

			secretsPath, createdSecrets, err := config.EnsureSecretsFile()
			if err != nil {
				return fmt.Errorf("ensuring secrets file: %w", err)
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			fmt.Printf("Data root: %s\n", cfg.DataRoot)
			if existed {
				fmt.Println("Config file: already present")
			} else {
				fmt.Println("Config file: written")
			}
			if createdSecrets {
				fmt.Printf("Secrets file: created at %s (edit in place, then set MEALCLI_USDA_API_KEY)\n", secretsPath)
			} else {
				fmt.Printf("Secrets file: already present at %s\n", secretsPath)
			}
			fmt.Println("Database: opened and migrated")
			return nil
		},
	}
}

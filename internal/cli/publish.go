package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/dashboard"
)

func newPublishCmd() *cobra.Command {
	var dryRun, noPush bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Render the dashboard and publish it to the dashboard-site repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if dryRun {
				tmpDir, err := os.MkdirTemp("", "meal-dashboard-preview-*")
				if err != nil {
					return fmt.Errorf("creating preview dir: %w", err)
				}
				if err := dashboard.Render(db, tmpDir); err != nil {
					return fmt.Errorf("rendering dashboard: %w", err)
				}
				fmt.Printf("Rendered preview to %s (not published)\n", tmpDir)
				fmt.Printf("Open: file://%s/index.html\n", tmpDir)
				return nil
			}

			siteDir := filepath.Join(cfg.DataRoot, "dashboard-site")
			if _, err := os.Stat(filepath.Join(siteDir, ".git")); err != nil {
				return fmt.Errorf(
					"%s is not a git repository yet.\n\n"+
						"This command never creates GitHub resources on its own. To set up the dashboard site:\n"+
						"  1. Create a repo (e.g. `gh repo create <name> --public --clone` or manually on GitHub),\n"+
						"     enable GitHub Pages for it (Settings -> Pages -> deploy from a branch).\n"+
						"  2. Clone it to exactly this path: %s\n"+
						"  3. Re-run `meal publish`.\n"+
						"Use --dry-run to preview the rendered site locally without any of this",
					siteDir, siteDir,
				)
			}

			if err := dashboard.Render(db, siteDir); err != nil {
				return fmt.Errorf("rendering dashboard: %w", err)
			}
			fmt.Printf("Rendered dashboard to %s\n", siteDir)

			if err := runGit(siteDir, "add", "-A"); err != nil {
				return err
			}
			commitMsg := fmt.Sprintf("Publish dashboard: %s", time.Now().Format("2006-01-02 15:04"))
			if err := runGit(siteDir, "commit", "-m", commitMsg); err != nil {
				fmt.Println("Nothing changed since last publish.")
				return nil
			}
			sha, _ := exec.Command("git", "-C", siteDir, "rev-parse", "--short", "HEAD").Output()
			fmt.Printf("Committed %s\n", string(sha))

			if noPush {
				fmt.Println("--no-push given, not pushing.")
				return nil
			}
			if err := runGit(siteDir, "push"); err != nil {
				return fmt.Errorf("pushing: %w (commit succeeded locally; push manually with `git -C %s push`)", err, siteDir)
			}
			fmt.Println("Pushed.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "render to a temp dir only, no git operations")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "render and commit, but don't push")
	return cmd
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

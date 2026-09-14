// Command mealcli is Patrick and Thea's personal meal-planning tool: pantry,
// recipes (synced from recipes.patrickandthea.com), consumption logging,
// grocery receipts, and analytics, backed by a local SQLite database.
package main

import "github.com/jpatrickb/meal-planning/internal/cli"

func main() {
	cli.Execute()
}

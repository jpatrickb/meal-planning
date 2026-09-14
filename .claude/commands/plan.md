# Generate Meal Plan

Create a meal plan based on current pantry stock, leftovers, health goals, budget, and recipe
variety - backed by the `meal` CLI, not markdown files. See `CLAUDE.md` for the full command
reference; this is the condensed workflow for this specific command.

## Workflow
1. **Check state:** `meal pantry list --expiring-within 7`, `meal leftovers list`,
   `meal analytics variety --limit 15` (surfaces neglected and highly-rated-but-underused recipes).
2. **Prioritize, in order:** leftovers > expiring perishables > pantry staples > recipe variety >
   entirely new recipes.
3. **Browse recipes:** `meal recipe list [--category X] [--tag X]`, then `meal recipe show <slug>`
   for anything you're considering, to check ingredients/instructions/serves against what's
   actually in the pantry.
4. **New recipe needed and nothing on recipes.patrickandthea.com fits?** Tell Patrick it's missing
   from the site rather than inventing a one-off recipe here - recipe content only lives on the
   site (see `CLAUDE.md`).
5. **Draft the plan:** `meal plan set --date D --slot breakfast|lunch|dinner|snack --recipe <slug>
   --for patrick|thea|both` for each meal. Re-running `set` for the same date+slot replaces it.
6. **Seed the shopping list:** `meal shopping-list build --from D --to D`, then read
   `meal shopping-list show`, cross-reference `meal pantry list`, and curate it yourself with
   `meal shopping-list add`/`check` - the build step just seeds raw ingredient text per recipe, it
   doesn't consolidate duplicates or net against pantry stock.

## Meal Requirements
- **Nutrition balance:** protein source + vegetable/fruit + healthy carb + healthy fat per meal.
- **Cost:** target roughly $5-8/meal depending on protein quality; check `meal recipe show` for any
  cost already set via `recipe set-meta`, and set it if missing.
- **No carb-only meals.**
- Rotate meat-based and plant-based proteins.

## Usage
Describe the timeframe and any constraints, e.g.:
> Make a plan for the next 3 days. I want to use up the eggs and chicken, and I'm avoiding dairy.

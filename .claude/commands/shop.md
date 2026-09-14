# Add Groceries

Log a grocery purchase (typed list or a pasted/photographed receipt) into pantry stock via the
`meal` CLI. See `CLAUDE.md` for the full command reference; this is the condensed workflow for
this specific command.

## Workflow
1. `meal shop start --store "..." --date YYYY-MM-DD [--receipt-total X]` → note the purchase id.
2. For each line item: `meal shop add-item --purchase <id> --raw "<verbatim text>" --qty Q --unit U
   --price P`. Leave `--item-id`/`--create-new` off first, so the alias matcher gets a chance:
   - **Auto-matched** (exact text or UPC seen before): nothing more to do for that line.
   - **Fuzzy match candidate:** reported, not yet stocked. Show Patrick the guess and confidence;
     if right, `meal alias confirm <alias-id> --item-id N`, then re-add the line with that
     `--item-id` (confirming doesn't retroactively fix the line already added).
   - **Unmatched:** ask what it is. Existing item → re-add with `--item-id N`. New item → re-add
     with `--create-new --name X --category Y --unit Z`.
3. `meal shop finish <id>` once you're done resolving lines - this is what actually creates on-hand
   pantry stock lots. Unmatched lines are reported, never silently stocked or dropped.
4. If anything on this receipt was on the shopping list, check it off:
   `meal shopping-list show` then `meal shopping-list check <id>`.

## Categories
`grains, protein_meat, protein_legume, protein_other, veg_fresh_frozen, veg_canned, fruit, baking,
oils_condiments, sauces, dairy, misc`

## Notes
- The first several receipts will need real back-and-forth on unmatched/fuzzy items - that's
  expected, not a sign something's broken. The alias dictionary is genuinely learning; it gets
  quieter as more receipt lines have been seen before.
- If an item's nutrition isn't known yet, that's handled at `meal log` time via `--grams`
  (automatic USDA lookup), not here.

## Usage
Describe what you bought, or paste/attach a receipt:
> I bought 2kg of chicken breast for $12.99, 1 dozen eggs for $4.50, and 3 cans of tuna for $5.70

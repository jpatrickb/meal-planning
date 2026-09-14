# mealcli

A single-household meal planning tool: pantry inventory, grocery receipts, what got
eaten, cost and macro tracking, and a static dashboard. Everything lives in one SQLite
database and is reached through the `meal` CLI.

It is built to be driven by a person **or** by an AI assistant working on their behalf.
That shapes the design more than anything else: the CLI records decisions, it does not
make them. Parsing a receipt line, deciding two foods are the same, judging how much of a
bag a bowl of cereal was — those are judgment calls that belong to whoever is driving.
The CLI's job is to persist the result durably and to **refuse rather than invent** when
it is asked for something it cannot know.

---

## Install

```sh
go build -o bin/meal ./cmd/mealcli
./bin/meal --help
```

Go 1.26+ (see `go.mod`). The only notable dependency is a pure-Go SQLite driver, so there is nothing to
install system-wide.

## Setup

```sh
./bin/meal init      # creates the data directory, database, and a config + secrets file
./bin/meal doctor    # verifies all of the above
```

`init` is safe to re-run. `doctor` is the thing to run whenever something seems off — it
checks the data root, database, applied migrations, USDA key presence (without printing
it), and how stale the recipe cache is. It also counts four data-quality gaps that each
quietly degrade a number downstream: pantry items with no nutrition source, cached USDA
foods that came back with no macro nutrients, logged entries with no macros, mapped
recipes with no serving count, and on-hand lots with no price. None is an error; each is
something to close.

**Where data lives.** Resolution order: `MEALCLI_DATA_ROOT` → `data_root` in
`~/.config/mealcli/config.toml` → `~/MealPlanning`. The directory holds the SQLite
database (`mealplanning.db`) and the recipe cache. It is deliberately outside this repo:
the code is version-controlled, the data is not.

**USDA API key.** Nutrition lookups use USDA FoodData Central. Get a free key at
<https://fdc.nal.usda.gov/api-key-signup.html> and put it in
`~/.config/mealcli/secrets.env` as `MEALCLI_USDA_API_KEY=...` (or set that env var). The
file is created with mode 600 by `init`. **Never commit a key** — if one ever appears in a
tracked file, treat it as a live incident, rotate it, and purge it.

Every command that produces a result takes `--json` for machine-readable output — including
the ones whose human output is a single confirmation line, because a script needs the ids
inside it (`shop start` hands back the purchase id every later `add-item` call needs).
`init` and `publish` report progress the same way either way.

---

## How it fits together

```
recipes.patrickandthea.com ──sync──▶ recipe cache (read-only content)
                                          │
grocery receipt ──shop──▶ pantry stock ───┼──▶ cook ──▶ leftovers ──▶ log
                              │           │              │
                              └───────────┴──────────────┴──▶ consumption log
                                                                  │
                                              analytics ◀─────────┘
                                                  │
                                              publish ──▶ static dashboard
```

### Recipes are read-only here

Recipe *content* — ingredients, instructions, tags — lives at
**recipes.patrickandthea.com**, not in this database. `meal sync recipes` fetches its
public JSON export into a local cache. There is deliberately no way to write recipe
content back from here; a missing recipe is a gap to report, not to fill locally.

*Local* data about a recipe — your rating, a serving-count override, a manual cost or
macro figure, and the ingredient→pantry mapping — lives in this database and survives
every sync.

### Pantry stock is lots, not totals

Buying an item creates a **lot**: a quantity, a unit, what it actually cost, when it was
acquired, and when it expires. Consumption draws down lots oldest-first (FIFO), so cost
and expiry follow the specific stock that was used. An item can hold several lots at once,
including lots recorded in different units.

### Costs and macros: three honest states

Both resolve the same way, and both can say "I don't know":

| State | When |
|---|---|
| **manual** | Someone set a value with `recipe set-meta`. Always wins. |
| **computed** | The ingredient mapping is complete *and* every ingredient contributed. |
| **unknown** | Anything less — reported with the specific reason, never as `0`. |

A partial sum presented as a total is the failure mode this design exists to prevent.

### Units

Quantities carry units, and they are compared honestly. Common spellings fold together
(`c`, `cup`, `cups`, `Cup` are one unit). Volume-to-volume and mass-to-mass arithmetic is
built in (tsp/Tbsp/cup/fl oz/pint/quart/gallon/ml/l, mg/g/kg/oz/lb). Anything that crosses
between them — a cup of flour weighing 120 g — is a property of the specific food and must
be recorded:

```sh
meal pantry set-conversion --item-id 21 --from cup --to g --factor 120 --estimated
```

One anchor per item is usually enough: `cup → g` also answers `Tbsp → g` and `cup → oz`
for that item, because arithmetic can run on either side of a recorded fact. Two recorded
facts are never chained — each is a separate measurement, and composing them compounds
error nobody agreed to. `--estimated` marks a standard reference figure rather than
something measured, so it can be revisited later.

Conversions are what let a cost lookup and a pantry decrement reach stock recorded in a
different unit than the one being asked for. Without a usable conversion, both refuse
rather than mix units.

---

## Command reference

### Logging what was eaten

```sh
# Cooked something and ate some now
meal cook <slug> --eaten-now patrick=1,thea=1 --meal-slot dinner

# Leftovers from an earlier cook (cost is always $0 — the batch was charged at cook time)
meal log "<description>" --person patrick --from-leftovers latest --servings 1

# A raw pantry item, not through a recipe
meal log "Banana" --person thea --deplete-pantry "162=1" --meal-slot snack

# Something never in the pantry (eating out, a protein bar)
meal log "<description>" --person patrick --cost 4.50 --grams 60
```

`cook` decrements real pantry stock for every mapped ingredient and records which lots it
drew from. Insufficient stock fails the whole cook rather than guessing; `--accept-zero-stock
<item-id>` proceeds anyway for one named item. `--scale 2` doubles the batch;
`--ingredient-qty "5=0.5"` overrides one ingredient for a cook that varied.

`--deplete-pantry "12=1,7=200:g"` draws real stock and computes both cost and macros from
the items named — no `--grams` needed, no USDA round trip. The `:unit` suffix is only
needed when it differs from the item's default unit.

`--grams` on an ad-hoc entry triggers a nutrition lookup. If your wording hasn't been seen
before but something close has, **the command blocks and shows candidates instead of
guessing** — text similarity cannot tell a chicken breast from a chicken thigh. Re-run
with `--reuse-fdc-id <id>` if it really is the same food (the phrasing is remembered), or
`--force-fresh-lookup` if it isn't.

A shared meal is one row per person, never a single row for both. Analytics depend on it.

```sh
meal log set-macros <id> --calories 447 --protein 15.7 ...   # fix an entry's macros in place
meal log void <id>                                            # delete it, putting back the stock it drew from
```

### Groceries

```sh
meal shop start --store "Winco" --date 2026-08-24          # → purchase id
meal shop add-item --purchase 3 --raw "<verbatim receipt text>" --qty 1 --unit bag --price 7.97
meal shop finish 3                                          # creates the pantry stock lots
```

`add-item` tries to match each line against known items and past aliases: exact, then
fuzzy, then unmatched. **Read the response.** A fuzzy match is a *candidate*, not a
decision — confirm it with `meal alias confirm <id> --item-id N`, or re-run the line with
an explicit `--item-id`. An unmatched line needs `--create-new --name X --category Y --unit Z`.

The alias dictionary genuinely learns: the first few receipts need real back-and-forth,
and it gets quieter as more lines have been seen before.

Categories: `grains, protein_meat, protein_legume, protein_other, veg_fresh_frozen,
veg_canned, fruit, baking, oils_condiments, sauces, dairy, misc`.

**Log the package, not one unit of it.** A line reading "Chicken Nuggets, 32 oz" entered as
`--qty 1 --unit oz` puts the whole line's price on a single ounce, and nothing downstream can
tell that from a genuine $5.97-per-ounce product. `add-item` warns when the receipt text
states a size that disagrees with `--qty`. Otherwise, log the unit the receipt actually
states, and never force-convert to match the item's default unit. Prefer a unit that is genuinely invariant for the product — weight for
anything sold in inconsistent package sizes.

### Pantry

```sh
meal pantry list [--all] [--expiring-within 7]
meal pantry adjust --item-id 197 --qty -0.05 --reason "..."   # +adds a lot, -consumes FIFO
meal pantry set-shelf-life --item-id 197 --days 365 --estimated
meal pantry set-conversion --item-id 21 --from cup --to g --factor 120 --estimated
meal pantry link-nutrition --item-id 15 [--all] [--force --query "butter, salted"] [--clear]
meal pantry set-nutrition --item-id 212 --serving-grams 44 --calories 140 --protein 25 ...
meal pantry merge --from <dup-id> --into <keep-id>
meal pantry set-lot-price --lot-id 117 --price 0.040094
```

`link-nutrition` matches a pantry item to USDA data automatically. **It is not perfectly
reliable** — a short generic name doesn't always rank the obvious entry first. Spot-check
the results and correct anything wrong with `--force --query "<more specific text>"`. Use
`--clear` to leave a genuinely ambiguous item honestly unlinked rather than force a wrong
match.

`set-nutrition` records macros straight off a product label and **takes precedence over the
USDA link** — the right answer for a branded item USDA has no entry for. `--estimated`
covers a seasoning blend where the numbers are a reasonable figure rather than a label.

### Recipes

```sh
meal sync recipes
meal recipe list [--category X] [--tag X]
meal recipe show <slug>
meal recipe set-meta <slug> --serves 4 --rating 5 --cost 12.40 --calories 650 ...
meal recipe set-meta <slug> --clear-cost --clear-macros    # let the computed values take over
meal recipe map-ingredients <slug> [--line N --item-id M --qty Q --line-unit U]
meal recipe backfill-macros [--recompute] [--dry-run]
```

`map-ingredients` with no `--line` shows the current mapping status; with `--line N` plus
`--item-id`/`--create-new`/`--not-tracked` it resolves one line. A recipe stays unmapped
until someone does this, and `meal cook` never blocks on it.

A cook event's macros come from **what that cook actually consumed, divided by the servings
it actually yielded** — not the recipe in the abstract. Those diverge whenever a batch is
scaled, has an ingredient overridden, or yields a different number of servings than the
recipe claims, and using the recipe would have a serving eaten at the table disagree with
the same serving eaten as a leftover.

### Planning and shopping list

```sh
meal plan set --date 2026-08-25 --slot dinner --recipe chili-lime-chicken-bowls --for both
meal plan show --from 2026-08-24 --to 2026-08-31
meal shopping-list build --from 2026-08-24 --to 2026-08-31
meal shopping-list show | add "<item>" | check <id> | remove <id> | clear
```

`build` seeds the list from planned recipes' raw ingredient text. **It is a starting point,
not a finished list** — it doesn't know what's already in the pantry and can't consolidate
overlapping ingredients. Curating it is a human (or assistant) job.

Write curated items as `SECTION: item text`. The dashboard groups by that uppercase prefix
so the list reads aisle-by-aisle on a phone. The prefix counts when it is uppercase, under
24 characters, and followed by `": "`. Put the reason in a parenthetical so the line
explains itself in the store.

`remove` deletes a line added by mistake. `check` means "bought it" — using it to tidy away
a bad line records a purchase that never happened.

### Waste, targets, analytics

```sh
meal waste log --item-id 51 --qty 1 --reason spoiled|expired|disliked|other
meal waste log --cook-event-id 3 --qty 2 --reason too_much_cooked|disliked|other
meal target set --person patrick --kcal 2400 --protein 180
meal analytics cost --period week [--person patrick]
meal analytics macros --period week [--person patrick]
meal analytics waste --period month
meal analytics variety [--limit 20]
```

`--person` narrows `cost` and `macros`; `waste` records what was discarded and why, not who
would have eaten it, so there is nothing to filter on there. Waste cost is derived from the
actual purchase price (item waste) or the recipe's per-serving cost (leftover waste) unless
`--cost` says otherwise. There is no "close the
week" step — SQLite keeps full history, so query whatever period is relevant.

### Nutrition maintenance

```sh
meal nutrition repair [--dry-run]
```

Re-reads every cached USDA food from the raw response stored alongside it and corrects
both the cache and any consumption entry computed from it. It re-derives from stored JSON
rather than re-querying, so a food cannot silently turn into a different food. It also
lists cached foods USDA returned with no macro nutrients at all — those store as a
confident-looking row of zeroes, which is not the same as a food that genuinely has none.

### Dashboard

```sh
meal publish [--dry-run]
```

Renders an 8-page static site, commits, and pushes to the dashboard repo; Vercel deploys on
push. `--dry-run` renders locally without touching git. The dashboard is a **snapshot** —
nothing updates it automatically, so run `publish` at the end of any session that wrote
something. It is read-only by design: there is no write path from the phone.

---

## Design rules

These are load-bearing. Breaking them corrupts data quietly, which is worse than an error.

1. **Never fabricate a serving count, a macro value, or an item match.** Every one of
   these paths is built to error and explain rather than guess. If `cook` can't resolve
   servings, ask. If a receipt line has no confident match, ask.
2. **Cost is charged once.** `cook` books the full batch cost; every `--from-leftovers`
   log is forced to $0 in code. Macros are *not* treated this way — eating a leftover
   serving delivers its calories again.
3. **Per-person, never household-only.** Every consumption entry names a person; a shared
   meal is two entries.
4. **An estimated quantity is a guess, not a measurement.** When a log depletes a fraction
   of a package, anchor that fraction to something real — package size, servings per
   container — and say what it was based on. Nothing downstream re-checks it.
5. **Recipe content is read-only here.** It belongs on the recipe site.

## Repository layout

```
cmd/mealcli/        entry point
internal/cli/       cobra commands — flag parsing, output, and the judgment-call prompts
internal/domain/    the rules: cost/macro resolution, unit conversion, matching
internal/store/     SQLite access and schema migrations (embedded, applied on open)
internal/usda/      USDA FoodData Central client
internal/dashboard/ static site rendering
docs/               design requirements
```

Migrations in `internal/store/migrations/` are embedded and applied automatically when the
database is opened. Add a new numbered file; never edit one that has shipped.

```sh
go test ./...
go vet ./...
```

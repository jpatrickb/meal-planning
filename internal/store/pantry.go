package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// PantryItem is a canonical grocery item. Stock is tracked separately, as
// lots in pantry_stock, so an item can have zero-or-more on-hand batches.
type PantryItem struct {
	ID          int64
	Name        string
	Category    string
	DefaultUnit string
	FDCID       *int
}

// CreatePantryItem inserts a new canonical item. Fails with a unique
// constraint error if the name already exists - callers should check
// GetPantryItemByName first when that's ambiguous (e.g. alias matching).
func CreatePantryItem(db *sql.DB, name, category, defaultUnit string) (int64, error) {
	res, err := db.Exec(`INSERT INTO pantry_items (name, category, default_unit) VALUES (?, ?, ?)`, name, category, defaultUnit)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetPantryItemByName looks up an item by exact name match.
func GetPantryItemByName(db *sql.DB, name string) (PantryItem, bool, error) {
	var it PantryItem
	err := db.QueryRow(`SELECT id, name, category, default_unit, fdc_id FROM pantry_items WHERE name = ?`, name).
		Scan(&it.ID, &it.Name, &it.Category, &it.DefaultUnit, &it.FDCID)
	if err == sql.ErrNoRows {
		return PantryItem{}, false, nil
	}
	if err != nil {
		return PantryItem{}, false, err
	}
	return it, true, nil
}

// SetPantryItemFDCID links an item to a USDA food, for computed macros.
func SetPantryItemFDCID(db *sql.DB, itemID int64, fdcID int) error {
	_, err := db.Exec(`UPDATE pantry_items SET fdc_id = ? WHERE id = ?`, fdcID, itemID)
	return err
}

// ClearPantryItemFDCID removes a (bad) nutrition link, leaving the item
// honestly unlinked rather than pointed at the wrong food.
func ClearPantryItemFDCID(db *sql.DB, itemID int64) error {
	_, err := db.Exec(`UPDATE pantry_items SET fdc_id = NULL WHERE id = ?`, itemID)
	return err
}

// ListPantryItemsMissingFDCID returns every item with no linked USDA food
// yet, for bulk nutrition-linking.
func ListPantryItemsMissingFDCID(db *sql.DB) ([]PantryItem, error) {
	rows, err := db.Query(`SELECT id, name, category, default_unit, fdc_id FROM pantry_items WHERE fdc_id IS NULL ORDER BY category, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PantryItem
	for rows.Next() {
		var it PantryItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Category, &it.DefaultUnit, &it.FDCID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetPantryItem fetches an item by id.
func GetPantryItem(db *sql.DB, id int64) (PantryItem, error) {
	var it PantryItem
	err := db.QueryRow(`SELECT id, name, category, default_unit, fdc_id FROM pantry_items WHERE id = ?`, id).
		Scan(&it.ID, &it.Name, &it.Category, &it.DefaultUnit, &it.FDCID)
	return it, err
}

// PantryStock is one lot (batch) of an item.
type PantryStock struct {
	ID                   int64
	ItemID               int64
	Quantity             float64
	Unit                 string
	UnitCost             *float64
	AcquiredDate         string
	ExpiresDate          *string
	SourcePurchaseItemID *int64
	Status               string
}

// AddPantryStock inserts a new on-hand lot, inside tx.
func AddPantryStock(tx *sql.Tx, s PantryStock) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO pantry_stock (item_id, quantity, unit, unit_cost, acquired_date, expires_date, source_purchase_item_id, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'on_hand')
	`, s.ItemID, s.Quantity, s.Unit, s.UnitCost, s.AcquiredDate, s.ExpiresDate, s.SourcePurchaseItemID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ErrInsufficientStock means a decrement would need more than is on hand.
var ErrInsufficientStock = errors.New("not enough stock on hand")

// ConsumedLot is one pantry_stock lot's contribution to a FIFO decrement --
// which lot, how much was taken from it, and that lot's own unit_cost (for
// callers, like meal cook, that need to record which specific lot an
// ingredient's consumption came from).
type ConsumedLot struct {
	StockID int64
	// Quantity and Unit describe the debit in the LOT's own unit, which is
	// what unit_cost is priced in and what the stored quantity was reduced
	// by. QuantityAsked is the same debit expressed in the unit the caller
	// asked for, which is the only footing on which lots in different units
	// can be added together. They differ exactly when a conversion was used.
	Quantity      float64
	Unit          string
	QuantityAsked float64
	UnitCost      *float64
}

// decrementLotsFIFO is the shared core behind DecrementPantryStockFIFO,
// DecrementPantryStockFIFODetailed, and DecrementPantryStockFIFOPartial:
// consumes up to qty of an item's on-hand stock, oldest lots first, marking
// each lot resultStatus once it hits zero.
//
// A lot recorded in a different unit than the one asked for is used only
// when convert can put the two on the same footing from a recorded
// conversion fact - an item legitimately carries stock in more than one unit
// at once (onions bought by weight, referenced by count), and summing across
// units without a real factor would produce a meaningless number. Lots that
// can't be converted are left untouched, exactly as before. Each lot is
// always debited in its OWN unit, so a conversion is applied once, on the
// way in, rather than being round-tripped through the stored quantity.
//
// If allowPartial is false, refuses (without writing anything) when the
// convertible on-hand total is less than qty. If allowPartial is true,
// consumes whatever is reachable -- which may be less than qty, or zero --
// and never errors for insufficient stock; callers compare the summed
// Quantity in the result against qty to see the shortfall.
func decrementLotsFIFO(tx *sql.Tx, itemID int64, unit string, qty float64, resultStatus string, allowPartial bool, convert UnitConverter) ([]ConsumedLot, error) {
	rows, queryErr := tx.Query(`
		SELECT id, quantity, unit, unit_cost FROM pantry_stock
		WHERE item_id = ? AND status = 'on_hand'
		ORDER BY acquired_date ASC, id ASC
	`, itemID)
	if queryErr != nil {
		return nil, queryErr
	}
	type lot struct {
		id       int64
		qty      float64
		unit     string
		unitCost *float64
		// qtyAsked is this lot's quantity expressed in the unit the caller
		// asked for, which is the only footing on which lots in different
		// units can be compared or summed.
		qtyAsked float64
	}
	var lots []lot
	for rows.Next() {
		var l lot
		if err := rows.Scan(&l.id, &l.qty, &l.unit, &l.unitCost); err != nil {
			rows.Close()
			return nil, err
		}
		if l.unit == unit {
			l.qtyAsked = l.qty
		} else if convert != nil {
			converted, ok := convert(l.qty, l.unit, unit)
			if !ok {
				continue
			}
			l.qtyAsked = converted
		} else {
			continue
		}
		lots = append(lots, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var available float64
	for _, l := range lots {
		available += l.qtyAsked
	}
	if !allowPartial && qty > available+1e-9 {
		return nil, fmt.Errorf("%w: item %d has %.2f %s on hand, asked to remove %.2f %s", ErrInsufficientStock, itemID, available, unit, qty, unit)
	}
	target := qty
	if target > available {
		target = available
	}

	remaining := target
	var consumed []ConsumedLot
	for _, l := range lots {
		if remaining <= 1e-9 {
			break
		}
		var portion float64
		if l.qtyAsked <= remaining+1e-9 {
			portion = l.qty
			if _, err := tx.Exec(`UPDATE pantry_stock SET quantity = 0, status = ? WHERE id = ?`, resultStatus, l.id); err != nil {
				return nil, err
			}
			remaining -= l.qtyAsked
		} else {
			portion = remaining
			if l.unit != unit {
				inLotUnit, ok := convert(remaining, unit, l.unit)
				if !ok {
					// Converting one way but not the other would leave the
					// lot debited by a quantity in the wrong unit.
					continue
				}
				portion = inLotUnit
			}
			if _, err := tx.Exec(`UPDATE pantry_stock SET quantity = quantity - ? WHERE id = ?`, portion, l.id); err != nil {
				return nil, err
			}
			remaining = 0
		}
		asked := portion
		if l.unit != unit {
			converted, ok := convert(portion, l.unit, unit)
			if !ok {
				return nil, fmt.Errorf("item %d: converted %s -> %s to debit lot %d but not back again", itemID, unit, l.unit, l.id)
			}
			asked = converted
		}
		consumed = append(consumed, ConsumedLot{
			StockID: l.id, Quantity: portion, Unit: l.unit, QuantityAsked: asked, UnitCost: l.unitCost,
		})
	}
	return consumed, nil
}

// DecrementPantryStockFIFO consumes qty of an item's on-hand stock, oldest
// lots first, marking each lot resultStatus ('consumed' or 'discarded') once
// it hits zero. Lots recorded in another unit count only when convert can
// bridge the two (see decrementLotsFIFO). Errors (without writing) if the
// reachable on-hand total is less than qty, inside tx. Returns the known cost
// of what was decremented (sum of unit_cost * portion, for lots that have a
// unit_cost) and whether any lot actually contributed a known cost - a
// partial sum when some lots lack cost data is still more informative than
// nothing, so callers should treat hasCost=true as "at least this much",
// not "exactly this much".
func DecrementPantryStockFIFO(tx *sql.Tx, itemID int64, unit string, qty float64, resultStatus string, convert UnitConverter) (knownCost float64, hasCost bool, err error) {
	lots, err := decrementLotsFIFO(tx, itemID, unit, qty, resultStatus, false, convert)
	if err != nil {
		return 0, false, err
	}
	for _, l := range lots {
		if l.UnitCost != nil {
			knownCost += *l.UnitCost * l.Quantity
			hasCost = true
		}
	}
	return knownCost, hasCost, nil
}

// DecrementPantryStockFIFODetailed is like DecrementPantryStockFIFO but
// returns the individual lots consumed, for callers (meal cook) that need to
// record which specific lot(s) an ingredient's consumption came from.
func DecrementPantryStockFIFODetailed(tx *sql.Tx, itemID int64, unit string, qty float64, resultStatus string, convert UnitConverter) ([]ConsumedLot, error) {
	return decrementLotsFIFO(tx, itemID, unit, qty, resultStatus, false, convert)
}

// DecrementPantryStockFIFOPartial is like DecrementPantryStockFIFODetailed
// but never errors for insufficient stock -- it consumes whatever is on hand
// in this unit (which may be less than qty, or zero). Used when a mapped
// ingredient has less on-hand stock than a recipe needs and the caller has
// explicitly decided to proceed anyway
// (docs/requirements-ingredient-costing.md §2: this is a per-item, per-cook
// decision, never a silent default).
func DecrementPantryStockFIFOPartial(tx *sql.Tx, itemID int64, unit string, qty float64, resultStatus string, convert UnitConverter) ([]ConsumedLot, error) {
	return decrementLotsFIFO(tx, itemID, unit, qty, resultStatus, true, convert)
}

// OtherUnitStock reports on-hand stock for an item recorded in units other
// than the one asked for, so an insufficient-stock error can tell the
// difference between "genuinely nothing on hand" and "stock exists, but in
// a unit no recorded conversion can reach from this one" -- a real, expected case
// per docs/requirements-ingredient-costing.md §11 (a recipe's unit and a
// purchase's unit legitimately differ; e.g. onions purchased by weight but
// referenced by count in a recipe).
func OtherUnitStock(db *sql.DB, itemID int64, excludeUnit string) ([]PantryOnHand, error) {
	rows, err := db.Query(`
		SELECT ps.unit, SUM(ps.quantity)
		FROM pantry_stock ps
		WHERE ps.item_id = ? AND ps.status = 'on_hand' AND ps.unit != ?
		GROUP BY ps.unit
		HAVING SUM(ps.quantity) > 0
	`, itemID, excludeUnit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PantryOnHand
	for rows.Next() {
		var p PantryOnHand
		p.ItemID = itemID
		if err := rows.Scan(&p.Unit, &p.Quantity); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListAllPantryItems returns the full item catalog, regardless of on-hand
// stock -- unlike ListPantryOnHand, which only shows items with a nonzero
// lot. Needed for browsing/reuse-checking the catalog while it's being built
// (e.g. bulk ingredient mapping), before any purchases exist to stock it.
func ListAllPantryItems(db *sql.DB) ([]PantryItem, error) {
	rows, err := db.Query(`SELECT id, name, category, default_unit, fdc_id FROM pantry_items ORDER BY category, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PantryItem
	for rows.Next() {
		var it PantryItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Category, &it.DefaultUnit, &it.FDCID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// PantryOnHand is one item's aggregated on-hand stock, in one unit. An item
// with lots recorded in more than one unit (e.g. onions bought once by
// weight, once by count) gets one row per unit -- summing across units would
// produce a meaningless number, so this never does that.
type PantryOnHand struct {
	ItemID         int64
	Name           string
	Category       string
	Unit           string
	Quantity       float64
	EarliestExpiry *string
}

// ListPantryOnHand aggregates on-hand quantity per (item, unit) across lots.
func ListPantryOnHand(db *sql.DB) ([]PantryOnHand, error) {
	rows, err := db.Query(`
		SELECT pi.id, pi.name, pi.category, ps.unit, SUM(ps.quantity), MIN(ps.expires_date)
		FROM pantry_items pi
		JOIN pantry_stock ps ON ps.item_id = pi.id AND ps.status = 'on_hand'
		GROUP BY pi.id, ps.unit
		HAVING SUM(ps.quantity) > 0
		ORDER BY pi.category, pi.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PantryOnHand
	for rows.Next() {
		var p PantryOnHand
		if err := rows.Scan(&p.ItemID, &p.Name, &p.Category, &p.Unit, &p.Quantity, &p.EarliestExpiry); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetItemUnitCost returns the simple average unit_cost across every
// pantry_stock lot ever recorded for an item in this unit (any lot
// status, not just on-hand -- historical price is what's needed, not
// current stock). A simple average, not weighted by remaining quantity:
// once a lot is partially consumed, its current quantity no longer reflects
// how much was originally bought at that price, so weighting by it would
// skew the result. Lots recorded in a different unit are not converted or
// mixed in. Returns ok=false if no lot has ever been recorded in this unit
// -- never fabricate a cost from nothing.
func GetItemUnitCost(db *sql.DB, itemID int64, unit string, convert UnitConverter) (float64, bool, error) {
	var avg sql.NullFloat64
	err := db.QueryRow(`
		SELECT AVG(unit_cost) FROM pantry_stock WHERE item_id = ? AND unit = ? AND unit_cost IS NOT NULL
	`, itemID, unit).Scan(&avg)
	if err != nil {
		return 0, false, err
	}
	if avg.Valid {
		return avg.Float64, true, nil
	}
	if convert == nil {
		return 0, false, nil
	}

	// Nothing was ever bought in this unit. A price recorded in another unit
	// still answers the question when there's a conversion fact between the
	// two: what one `unit` costs is what its worth in the priced unit costs.
	rows, err := db.Query(`
		SELECT unit, AVG(unit_cost) FROM pantry_stock
		WHERE item_id = ? AND unit_cost IS NOT NULL
		GROUP BY unit
	`, itemID)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var pricedUnit string
		var pricedAvg float64
		if err := rows.Scan(&pricedUnit, &pricedAvg); err != nil {
			return 0, false, err
		}
		if perUnit, ok := convert(1, unit, pricedUnit); ok {
			return pricedAvg * perUnit, true, nil
		}
	}
	return 0, false, rows.Err()
}

// ExpiringStock is one lot expiring soon, from v_pantry_expiring.
type ExpiringStock struct {
	StockID         int64
	ItemName        string
	Category        string
	Quantity        float64
	Unit            string
	ExpiresDate     string
	DaysUntilExpiry int
}

// ListExpiringStock returns on-hand lots expiring within the next
// withinDays days (withinDays <= 0 means "no lower bound", i.e. include
// already-expired lots too, but still requires a non-null expires_date).
func ListExpiringStock(db *sql.DB, withinDays int) ([]ExpiringStock, error) {
	query := `SELECT stock_id, item_name, category, quantity, unit, expires_date, days_until_expiry FROM v_pantry_expiring`
	var args []any
	if withinDays > 0 {
		query += ` WHERE days_until_expiry <= ?`
		args = append(args, withinDays)
	}
	query += ` ORDER BY expires_date ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ExpiringStock
	for rows.Next() {
		var e ExpiringStock
		if err := rows.Scan(&e.StockID, &e.ItemName, &e.Category, &e.Quantity, &e.Unit, &e.ExpiresDate, &e.DaysUntilExpiry); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PantryItemNamesByFDCID maps each linked USDA food id to the pantry items
// pointing at it, so a problem with a cached food can be reported in terms
// of the items it actually affects.
func PantryItemNamesByFDCID(db *sql.DB) (map[int][]string, error) {
	rows, err := db.Query(`SELECT fdc_id, name FROM pantry_items WHERE fdc_id IS NOT NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int][]string{}
	for rows.Next() {
		var fdcID int
		var name string
		if err := rows.Scan(&fdcID, &name); err != nil {
			return nil, err
		}
		out[fdcID] = append(out[fdcID], name)
	}
	return out, rows.Err()
}

// MovedLot describes one pantry_stock lot repointed to a different item.
type MovedLot struct {
	StockID      int64   `json:"stock_id"`
	FromItemID   int64   `json:"from_item_id"`
	FromItemName string  `json:"from_item_name"`
	ToItemID     int64   `json:"to_item_id"`
	ToItemName   string  `json:"to_item_name"`
	Quantity     float64 `json:"quantity"`
	Unit         string  `json:"unit"`
	UnitMismatch string  `json:"unit_mismatch,omitempty"`
}

// MoveStockLot repoints one on-hand lot to a different pantry item, keeping
// its quantity, unit, price, acquired date and expiration intact.
//
// This is the fix for a receipt line that got matched to the wrong item and
// only turned out wrong after `shop finish` created the stock - splitting one
// lot back out of an item it never belonged to. `pantry merge` can only fold
// two items together, and doing it with `pantry adjust` would mark the lot
// 'consumed', recording an eating that never happened.
func MoveStockLot(db *sql.DB, stockID, toItemID int64) (MovedLot, error) {
	var m MovedLot
	m.StockID, m.ToItemID = stockID, toItemID

	var status string
	err := db.QueryRow(`
		SELECT ps.item_id, ps.quantity, ps.unit, ps.status, pi.name
		FROM pantry_stock ps JOIN pantry_items pi ON pi.id = ps.item_id
		WHERE ps.id = ?`, stockID).
		Scan(&m.FromItemID, &m.Quantity, &m.Unit, &status, &m.FromItemName)
	if err == sql.ErrNoRows {
		return m, fmt.Errorf("no pantry stock lot with id %d", stockID)
	}
	if err != nil {
		return m, err
	}
	if m.FromItemID == toItemID {
		return m, fmt.Errorf("lot %d is already on item %d (%s)", stockID, toItemID, m.FromItemName)
	}

	var defaultUnit string
	err = db.QueryRow(`SELECT name, default_unit FROM pantry_items WHERE id = ?`, toItemID).
		Scan(&m.ToItemName, &defaultUnit)
	if err == sql.ErrNoRows {
		return m, fmt.Errorf("no pantry item with id %d", toItemID)
	}
	if err != nil {
		return m, err
	}
	// Lots carry their own unit, so a mismatch isn't fatal - but it will need
	// a conversion before this stock can answer a request in the item's
	// default unit, so say so rather than let it surface later as a decrement
	// that mysteriously can't find stock.
	if defaultUnit != m.Unit {
		m.UnitMismatch = defaultUnit
	}

	res, err := db.Exec(`UPDATE pantry_stock SET item_id = ? WHERE id = ?`, toItemID, stockID)
	if err != nil {
		return m, fmt.Errorf("moving lot %d to item %d: %w", stockID, toItemID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return m, fmt.Errorf("pantry stock lot %d was not moved", stockID)
	}
	return m, nil
}

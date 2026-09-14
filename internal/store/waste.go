package store

import (
	"database/sql"
	"fmt"
)

// WasteEntry mirrors waste_log. Exactly one of ItemID or CookEventID should
// be set - pantry waste (spoiled/expired ingredients) or leftover waste
// (cooked food that didn't get eaten), respectively.
type WasteEntry struct {
	ID            int64
	ItemID        *int64
	CookEventID   *int64
	Description   *string
	Quantity      *float64
	Unit          *string
	EstimatedCost *float64
	WastedDate    string
	Reason        string
}

func insertWasteLog(tx *sql.Tx, w WasteEntry) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO waste_log (item_id, cook_event_id, description, quantity, unit, estimated_cost, wasted_date, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, w.ItemID, w.CookEventID, w.Description, w.Quantity, w.Unit, w.EstimatedCost, w.WastedDate, w.Reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LogItemWaste discards qty of a pantry item's on-hand stock (FIFO,
// mirroring DecrementPantryStockFIFO's lots but marking them 'discarded'
// instead of 'consumed') and records a waste_log row. If explicitCost is
// nil, the cost is derived from the actual purchase cost of the discarded
// lots (when known) rather than left unexplained.
func LogItemWaste(db *sql.DB, itemID int64, qty float64, unit, reason, wastedDate string, description *string, explicitCost *float64, convert UnitConverter) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	derivedCost, hasCost, err := DecrementPantryStockFIFO(tx, itemID, unit, qty, "discarded", convert)
	if err != nil {
		return 0, err
	}

	cost := explicitCost
	if cost == nil && hasCost {
		cost = &derivedCost
	}

	id, err := insertWasteLog(tx, WasteEntry{
		ItemID: &itemID, Description: description, Quantity: &qty, Unit: &unit,
		EstimatedCost: cost, WastedDate: wastedDate, Reason: reason,
	})
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// LogLeftoverWaste discards qty of servings from an open cook event and
// records a waste_log row. If explicitCost is nil, cost is derived as the
// wasted share of that cook event's total recipe cost.
func LogLeftoverWaste(db *sql.DB, cookEventID int64, qty float64, reason, wastedDate string, description *string, explicitCost *float64) (int64, error) {
	ce, err := GetCookEvent(db, cookEventID)
	if err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := DecrementCookEventServings(tx, cookEventID, qty); err != nil {
		return 0, err
	}

	cost := explicitCost
	if cost == nil && ce.ServingsYielded > 0 {
		derived := ce.TotalCost / ce.ServingsYielded * qty
		cost = &derived
	}

	id, err := insertWasteLog(tx, WasteEntry{
		CookEventID: &cookEventID, Description: description, Quantity: &qty,
		EstimatedCost: cost, WastedDate: wastedDate, Reason: reason,
	})
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// VoidedWasteEntry describes a waste_log row removed by VoidWasteEntry and
// what the removal put back.
type VoidedWasteEntry struct {
	ID               int64         `json:"id"`
	ItemID           *int64        `json:"item_id,omitempty"`
	ItemName         string        `json:"item_name,omitempty"`
	CookEventID      *int64        `json:"cook_event_id,omitempty"`
	Quantity         float64       `json:"quantity"`
	Unit             string        `json:"unit,omitempty"`
	EstimatedCost    float64       `json:"estimated_cost"`
	WastedDate       string        `json:"wasted_date"`
	Reason           string        `json:"reason"`
	RestoredLots     []RestoredLot `json:"restored_lots,omitempty"`
	ServingsReturned float64       `json:"servings_returned,omitempty"`
	Unrestored       float64       `json:"unrestored,omitempty"`
}

// VoidWasteEntry reverses a waste_log row: an item waste puts its stock back
// on hand, a leftover waste gives its servings back to the cook event, and
// the row is deleted.
//
// Unlike a consumption void, waste records no per-lot ledger, so stock goes
// back onto the item's discarded lots most-recently-discarded first. That is
// exact for the case that matters - undoing a waste entry that was just
// logged in error - but it cannot tell two waste events of the same item
// apart, so void the most recent one first. Anything that can't be placed
// (the lots were in a different unit, or none are marked discarded) is
// reported in Unrestored rather than silently invented as a new lot.
func VoidWasteEntry(db *sql.DB, id int64) (VoidedWasteEntry, error) {
	var v VoidedWasteEntry
	var qty, cost sql.NullFloat64
	var unit sql.NullString
	err := db.QueryRow(`
		SELECT id, item_id, cook_event_id, quantity, unit, estimated_cost, wasted_date, reason
		FROM waste_log WHERE id = ?`, id).
		Scan(&v.ID, &v.ItemID, &v.CookEventID, &qty, &unit, &cost, &v.WastedDate, &v.Reason)
	if err == sql.ErrNoRows {
		return v, errNoWasteEntry(id)
	}
	if err != nil {
		return v, err
	}
	v.Quantity, v.Unit, v.EstimatedCost = qty.Float64, unit.String, cost.Float64

	tx, err := db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()

	switch {
	case v.CookEventID != nil:
		if _, err := tx.Exec(
			`UPDATE cook_events SET servings_remaining = servings_remaining + ? WHERE id = ?`,
			v.Quantity, *v.CookEventID); err != nil {
			return v, err
		}
		v.ServingsReturned = v.Quantity

	case v.ItemID != nil:
		if err := tx.QueryRow(`SELECT name FROM pantry_items WHERE id = ?`, *v.ItemID).Scan(&v.ItemName); err != nil && err != sql.ErrNoRows {
			return v, err
		}
		rows, err := tx.Query(`
			SELECT id, quantity, unit FROM pantry_stock
			WHERE item_id = ? AND status = 'discarded' AND unit = ?
			ORDER BY id DESC`, *v.ItemID, v.Unit)
		if err != nil {
			return v, err
		}
		type lot struct {
			id  int64
			qty float64
		}
		var lots []lot
		for rows.Next() {
			var l lot
			var u string
			if err := rows.Scan(&l.id, &l.qty, &u); err != nil {
				rows.Close()
				return v, err
			}
			lots = append(lots, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return v, err
		}

		remaining := v.Quantity
		for _, l := range lots {
			if remaining <= 1e-9 {
				break
			}
			// A discarded lot was zeroed by the waste; give back what this
			// void still owes, up to the whole of it.
			give := remaining
			if _, err := tx.Exec(
				`UPDATE pantry_stock SET quantity = quantity + ?, status = 'on_hand' WHERE id = ?`,
				give, l.id); err != nil {
				return v, err
			}
			v.RestoredLots = append(v.RestoredLots, RestoredLot{
				ItemID: *v.ItemID, ItemName: v.ItemName, StockID: l.id, Quantity: give, Unit: v.Unit,
			})
			remaining -= give
		}
		v.Unrestored = remaining
	}

	res, err := tx.Exec(`DELETE FROM waste_log WHERE id = ?`, id)
	if err != nil {
		return v, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return v, errNoWasteEntry(id)
	}
	return v, tx.Commit()
}

func errNoWasteEntry(id int64) error {
	return fmt.Errorf("no waste log entry with id %d", id)
}

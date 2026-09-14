package store

import (
	"database/sql"
	"fmt"
)

// ConsumptionDepletion is one pantry lot a logged entry drew from. Quantity
// and Unit are the lot's own, so restoring is a plain addition.
type ConsumptionDepletion struct {
	ID               int64
	ConsumptionLogID int64
	ItemID           int64
	PantryStockID    int64
	Quantity         float64
	Unit             string
}

// InsertConsumptionDepletion records one lot's contribution to a log entry,
// inside tx - the same transaction that decremented the lot, so the ledger
// can never disagree with the stock it describes.
func InsertConsumptionDepletion(tx *sql.Tx, d ConsumptionDepletion) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO consumption_depletions (consumption_log_id, item_id, pantry_stock_id, quantity, unit)
		VALUES (?, ?, ?, ?, ?)
	`, d.ConsumptionLogID, d.ItemID, d.PantryStockID, d.Quantity, d.Unit)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListConsumptionDepletions returns what one log entry drew from.
func ListConsumptionDepletions(db *sql.DB, consumptionLogID int64) ([]ConsumptionDepletion, error) {
	rows, err := db.Query(`
		SELECT id, consumption_log_id, item_id, pantry_stock_id, quantity, unit
		FROM consumption_depletions WHERE consumption_log_id = ? ORDER BY id
	`, consumptionLogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConsumptionDepletion
	for rows.Next() {
		var d ConsumptionDepletion
		if err := rows.Scan(&d.ID, &d.ConsumptionLogID, &d.ItemID, &d.PantryStockID, &d.Quantity, &d.Unit); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RestoreDepletedLot adds quantity back to the lot it came from, inside tx,
// flipping a fully-consumed lot back to on_hand.
//
// Putting stock back on the original lot (rather than creating a new one) is
// what makes a void a true undo: the lot keeps its acquired date, its
// expiration, and the price it was actually bought at, none of which a
// replacement lot could reproduce without someone guessing at them.
func RestoreDepletedLot(tx *sql.Tx, stockID int64, quantity float64) error {
	res, err := tx.Exec(`
		UPDATE pantry_stock SET quantity = quantity + ?, status = 'on_hand' WHERE id = ?
	`, quantity, stockID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("pantry stock lot %d not found", stockID)
	}
	return nil
}

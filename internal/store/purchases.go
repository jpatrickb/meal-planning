package store

import (
	"database/sql"
	"fmt"
)

// Purchase is one shopping trip/receipt.
type Purchase struct {
	ID           int64
	PurchaseDate string
	Store        *string
	ReceiptTotal *float64
	RawSource    *string
	Status       string
}

// CreatePurchase starts a new open purchase.
func CreatePurchase(db *sql.DB, p Purchase) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO purchases (purchase_date, store, receipt_total, raw_source, status)
		VALUES (?, ?, ?, ?, 'open')
	`, p.PurchaseDate, p.Store, p.ReceiptTotal, p.RawSource)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetPurchase fetches a purchase by id.
func GetPurchase(db *sql.DB, id int64) (Purchase, error) {
	var p Purchase
	err := db.QueryRow(`SELECT id, purchase_date, store, receipt_total, raw_source, status FROM purchases WHERE id = ?`, id).
		Scan(&p.ID, &p.PurchaseDate, &p.Store, &p.ReceiptTotal, &p.RawSource, &p.Status)
	return p, err
}

// PurchaseItem is one receipt line item, optionally matched to a pantry item.
type PurchaseItem struct {
	ID              int64
	PurchaseID      int64
	RawText         string
	UPC             *string
	MatchedItemID   *int64
	Quantity        *float64
	Unit            *string
	UnitPrice       *float64
	LineTotal       *float64
	MatchConfidence *float64
	MatchMethod     *string
}

// AddPurchaseItem inserts one receipt line item.
func AddPurchaseItem(db *sql.DB, pi PurchaseItem) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO purchase_items (
			purchase_id, raw_text, upc, matched_item_id, quantity, unit, unit_price, line_total,
			match_confidence, match_method
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, pi.PurchaseID, pi.RawText, pi.UPC, pi.MatchedItemID, pi.Quantity, pi.Unit, pi.UnitPrice, pi.LineTotal,
		pi.MatchConfidence, pi.MatchMethod)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPurchaseItems returns every line item for a purchase, in entry order.
func ListPurchaseItems(db *sql.DB, purchaseID int64) ([]PurchaseItem, error) {
	rows, err := db.Query(`
		SELECT id, purchase_id, raw_text, upc, matched_item_id, quantity, unit, unit_price, line_total,
		       match_confidence, match_method
		FROM purchase_items WHERE purchase_id = ? ORDER BY id
	`, purchaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PurchaseItem
	for rows.Next() {
		var pi PurchaseItem
		if err := rows.Scan(&pi.ID, &pi.PurchaseID, &pi.RawText, &pi.UPC, &pi.MatchedItemID, &pi.Quantity,
			&pi.Unit, &pi.UnitPrice, &pi.LineTotal, &pi.MatchConfidence, &pi.MatchMethod); err != nil {
			return nil, err
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}

// FinishPurchaseResult summarizes what finishing a purchase did.
type FinishPurchaseResult struct {
	StockedCount     int
	UnmatchedCount   int
	NoShelfLifeItems []string // names of newly-stocked items with no typical_shelf_life estimate yet
}

// FinishPurchase creates a pantry_stock lot for every matched line item
// (source_purchase_item_id linking back to it), marks the purchase
// 'finished', and reports how many items were stocked vs left unmatched.
// Unmatched items are never silently dropped - they just don't get stock.
func FinishPurchase(db *sql.DB, purchaseID int64) (FinishPurchaseResult, error) {
	purchase, err := GetPurchase(db, purchaseID)
	if err != nil {
		return FinishPurchaseResult{}, fmt.Errorf("loading purchase: %w", err)
	}
	items, err := ListPurchaseItems(db, purchaseID)
	if err != nil {
		return FinishPurchaseResult{}, fmt.Errorf("loading purchase items: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return FinishPurchaseResult{}, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	var result FinishPurchaseResult
	for _, item := range items {
		if item.MatchedItemID == nil {
			result.UnmatchedCount++
			continue
		}
		qty := 1.0
		if item.Quantity != nil {
			qty = *item.Quantity
		}
		matchedItem, err := GetPantryItem(db, *item.MatchedItemID)
		if err != nil {
			return FinishPurchaseResult{}, fmt.Errorf("loading matched item for %q: %w", item.RawText, err)
		}
		unit := matchedItem.DefaultUnit
		if item.Unit != nil && *item.Unit != "" {
			unit = *item.Unit
		}

		stock := PantryStock{
			ItemID: *item.MatchedItemID, Quantity: qty, Unit: unit, UnitCost: item.UnitPrice,
			AcquiredDate: purchase.PurchaseDate, SourcePurchaseItemID: &item.ID,
		}
		if life, ok, err := GetShelfLife(db, *item.MatchedItemID); err != nil {
			return FinishPurchaseResult{}, fmt.Errorf("checking shelf life for %q: %w", item.RawText, err)
		} else if ok {
			expires, err := EstimateExpiryDate(purchase.PurchaseDate, life.Days)
			if err != nil {
				return FinishPurchaseResult{}, fmt.Errorf("estimating expiry for %q: %w", item.RawText, err)
			}
			stock.ExpiresDate = &expires
		} else {
			result.NoShelfLifeItems = append(result.NoShelfLifeItems, matchedItem.Name)
		}

		if _, err := AddPantryStock(tx, stock); err != nil {
			return FinishPurchaseResult{}, fmt.Errorf("stocking %q: %w", item.RawText, err)
		}
		result.StockedCount++
	}

	if _, err := tx.Exec(`UPDATE purchases SET status = 'finished' WHERE id = ?`, purchaseID); err != nil {
		return FinishPurchaseResult{}, fmt.Errorf("marking purchase finished: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FinishPurchaseResult{}, fmt.Errorf("committing: %w", err)
	}
	return result, nil
}

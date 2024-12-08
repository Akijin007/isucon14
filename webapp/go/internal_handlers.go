package main

import (
	"database/sql"
	"errors"
	"net/http"
)

// このAPIをインスタンス内から一定間隔で叩かせることで、椅子とライドをマッチングさせる
func internalGetMatching(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 最大10件のリクエストを取得
	rides := []*Ride{}
	if err := db.SelectContext(ctx, &rides, `
		SELECT * 
		FROM rides 
		WHERE chair_id IS NULL 
		ORDER BY created_at 
		LIMIT 30
	`); err != nil {
		if errors.Is(err, sql.ErrNoRows) || len(rides) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 有効な chairs を取得
	chairs := []*Chair{}
	query := `
		SELECT chairs.* 
		FROM chairs
		INNER JOIN chair_models cm ON cm.name = chairs.model
		WHERE chairs.is_active = TRUE
		AND NOT EXISTS (
			SELECT 1
			FROM ride_statuses rs
			INNER JOIN rides r ON r.id = rs.ride_id
			WHERE r.chair_id = chairs.id
			GROUP BY rs.ride_id
			HAVING COUNT(rs.chair_sent_at) != 6
		)
		ORDER BY cm.speed DESC
		LIMIT 30;
	`
	if err := db.SelectContext(ctx, &chairs, query); err != nil {
		if errors.Is(err, sql.ErrNoRows) || len(chairs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// リクエストとチェアを順にマッチング
	tx, err := db.BeginTx(ctx, nil) // トランザクションを開始
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback() // エラー時はロールバック

	for i := 0; i < len(rides) && i < len(chairs); i++ {
		if _, err := tx.ExecContext(ctx, `
			UPDATE rides 
			SET chair_id = ? 
			WHERE id = ?`, chairs[i].ID, rides[i].ID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	// コミットして変更を確定
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

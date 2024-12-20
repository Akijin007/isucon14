package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	initialFare     = 500
	farePerDistance = 100
)

type ownerPostOwnersRequest struct {
	Name string `json:"name"`
}

type ownerPostOwnersResponse struct {
	ID                 string `json:"id"`
	ChairRegisterToken string `json:"chair_register_token"`
}

func ownerPostOwners(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &ownerPostOwnersRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("some of required fields(name) are empty"))
		return
	}

	ownerID := ulid.Make().String()
	accessToken := secureRandomStr(32)
	chairRegisterToken := secureRandomStr(32)

	now := time.Now()
	_, err := db.ExecContext(
		ctx,
		"INSERT INTO owners (id, name, access_token, chair_register_token, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		ownerID, req.Name, accessToken, chairRegisterToken, now, now,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	newOwner := Owner{
		ID:                 ownerID,
		Name:               req.Name,
		AccessToken:        accessToken,
		ChairRegisterToken: chairRegisterToken,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	ownerTokenCache.Store(accessToken, newOwner)

	http.SetCookie(w, &http.Cookie{
		Path:  "/",
		Name:  "owner_session",
		Value: accessToken,
	})

	writeJSON(w, http.StatusCreated, &ownerPostOwnersResponse{
		ID:                 ownerID,
		ChairRegisterToken: chairRegisterToken,
	})
}

type chairSales struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Sales int    `json:"sales"`
}

type modelSales struct {
	Model string `json:"model"`
	Sales int    `json:"sales"`
}

type ownerGetSalesResponse struct {
	TotalSales int          `json:"total_sales"`
	Chairs     []chairSales `json:"chairs"`
	Models     []modelSales `json:"models"`
}

func ownerGetSales(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Unix(0, 0)
	until := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if r.URL.Query().Get("since") != "" {
		parsed, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		since = time.UnixMilli(parsed)
	}
	if r.URL.Query().Get("until") != "" {
		parsed, err := strconv.ParseInt(r.URL.Query().Get("until"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		until = time.UnixMilli(parsed)
	}

	owner := r.Context().Value("owner").(*Owner)

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	chairs := []Chair{}
	if err := tx.SelectContext(ctx, &chairs, "SELECT * FROM chairs WHERE owner_id = ?", owner.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	res := ownerGetSalesResponse{
		TotalSales: 0,
	}

	modelSalesByModel := map[string]int{}
	for _, chair := range chairs {
		rides := []Ride{}
		if err := tx.SelectContext(ctx, &rides, "SELECT rides.* FROM rides JOIN ride_statuses ON rides.id = ride_statuses.ride_id WHERE chair_id = ? AND status = 'COMPLETED' AND updated_at BETWEEN ? AND ? + INTERVAL 999 MICROSECOND", chair.ID, since, until); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		sales := sumSales(rides)
		res.TotalSales += sales

		res.Chairs = append(res.Chairs, chairSales{
			ID:    chair.ID,
			Name:  chair.Name,
			Sales: sales,
		})

		modelSalesByModel[chair.Model] += sales
	}

	models := []modelSales{}
	for model, sales := range modelSalesByModel {
		models = append(models, modelSales{
			Model: model,
			Sales: sales,
		})
	}
	res.Models = models

	writeJSON(w, http.StatusOK, res)
}

func sumSales(rides []Ride) int {
	sale := 0
	for _, ride := range rides {
		sale += calculateSale(ride)
	}
	return sale
}

func calculateSale(ride Ride) int {
	return calculateFare(ride.PickupLatitude, ride.PickupLongitude, ride.DestinationLatitude, ride.DestinationLongitude)
}

type chairWithDetail struct {
	ID                     string       `db:"id"`
	OwnerID                string       `db:"owner_id"`
	Name                   string       `db:"name"`
	AccessToken            string       `db:"access_token"`
	Model                  string       `db:"model"`
	IsActive               bool         `db:"is_active"`
	CreatedAt              time.Time    `db:"created_at"`
	UpdatedAt              time.Time    `db:"updated_at"`
	TotalDistance          int          `db:"total_distance"`
	TotalDistanceUpdatedAt sql.NullTime `db:"total_distance_updated_at"`
}

type ownerGetChairResponse struct {
	Chairs []ownerGetChairResponseChair `json:"chairs"`
}

type ownerGetChairResponseChair struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Model                  string `json:"model"`
	Active                 bool   `json:"active"`
	RegisteredAt           int64  `json:"registered_at"`
	TotalDistance          int    `json:"total_distance"`
	TotalDistanceUpdatedAt *int64 `json:"total_distance_updated_at,omitempty"`
}

type TotalDistanceInfo struct {
	ID                     string       `db:"chair_id"`
	TotalDistance          int          `db:"total_distance"`
	TotalDistanceUpdatedAt sql.NullTime `db:"total_distance_updated_at"`
}

func getTotalDistance(id string) (TotalDistanceInfo, error) {
	query := `
				WITH location_distances AS (
					SELECT 
						chair_id,
						ABS(latitude - LAG(latitude) OVER (PARTITION BY chair_id ORDER BY created_at)) +
						ABS(longitude - LAG(longitude) OVER (PARTITION BY chair_id ORDER BY created_at)) AS distance,
						created_at
					FROM chair_locations
					WHERE chair_id = ? -- 特定の chair_id を指定
				)
				SELECT 
					chair_id,
					IFNULL(SUM(distance),0) AS total_distance,
					MAX(created_at) AS total_distance_updated_at -- 最新の updated_at を取得
				FROM location_distances
				GROUP BY chair_id`
	var totalDistance TotalDistanceInfo
	if err := db.Get(&totalDistance, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TotalDistanceInfo{
				ID:                     id,
				TotalDistance:          0,
				TotalDistanceUpdatedAt: sql.NullTime{Valid: false},
			}, nil
		}
		return TotalDistanceInfo{}, err
	}
	return totalDistance, nil
}

func ownerGetChairs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := ctx.Value("owner").(*Owner)

	var chairs []Chair
	query := "SELECT * FROM chairs WHERE owner_id = ?"
	if err := db.SelectContext(ctx, &chairs, query, owner.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	res := ownerGetChairResponse{}

	for _, chair := range chairs {
		distanceInfo, err := getTotalDistance(chair.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		c := ownerGetChairResponseChair{
			ID:            chair.ID,
			Name:          chair.Name,
			Model:         chair.Model,
			Active:        chair.IsActive,
			RegisteredAt:  chair.CreatedAt.UnixMilli(),
			TotalDistance: distanceInfo.TotalDistance,
		}
		if distanceInfo.TotalDistanceUpdatedAt.Valid {
			t := distanceInfo.TotalDistanceUpdatedAt.Time.UnixMilli()
			c.TotalDistanceUpdatedAt = &t
		}
		res.Chairs = append(res.Chairs, c)
	}
	writeJSON(w, http.StatusOK, res)
}

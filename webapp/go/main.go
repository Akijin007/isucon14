package main

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-sql-driver/mysql"
	"github.com/isucon/isucon14/webapp/go/isuutil"
	"github.com/jmoiron/sqlx"
	"github.com/kaz/pprotein/integration/standalone"
)

var db *sqlx.DB

func main() {
	mux := setup()
	slog.Info("Listening on :8080")
	go standalone.Integrate(":19001")
	http.ListenAndServe(":8080", mux)
}

func setup() http.Handler {
	host := os.Getenv("ISUCON_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("ISUCON_DB_PORT")
	if port == "" {
		port = "3306"
	}
	_, err := strconv.Atoi(port)
	if err != nil {
		panic(fmt.Sprintf("failed to convert DB port number from ISUCON_DB_PORT environment variable into int: %v", err))
	}
	user := os.Getenv("ISUCON_DB_USER")
	if user == "" {
		user = "isucon"
	}
	password := os.Getenv("ISUCON_DB_PASSWORD")
	if password == "" {
		password = "isucon"
	}
	dbname := os.Getenv("ISUCON_DB_NAME")
	if dbname == "" {
		dbname = "isuride"
	}

	dbConfig := mysql.NewConfig()
	dbConfig.User = user
	dbConfig.Passwd = password
	dbConfig.Addr = net.JoinHostPort(host, port)
	dbConfig.Net = "tcp"
	dbConfig.DBName = dbname
	dbConfig.ParseTime = true

	_db, err := sqlx.Connect("mysql", dbConfig.FormatDSN())
	if err != nil {
		panic(err)
	}
	db = _db

	initCache()

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	mux.Use(middleware.Recoverer)
	mux.HandleFunc("POST /api/initialize", postInitialize)

	// app handlers
	{
		mux.HandleFunc("POST /api/app/users", appPostUsers)

		authedMux := mux.With(appAuthMiddleware)
		authedMux.HandleFunc("POST /api/app/payment-methods", appPostPaymentMethods)
		authedMux.HandleFunc("GET /api/app/rides", appGetRides)
		authedMux.HandleFunc("POST /api/app/rides", appPostRides)
		authedMux.HandleFunc("POST /api/app/rides/estimated-fare", appPostRidesEstimatedFare)
		authedMux.HandleFunc("POST /api/app/rides/{ride_id}/evaluation", appPostRideEvaluatation)
		authedMux.HandleFunc("GET /api/app/notification", appGetNotification)
		authedMux.HandleFunc("GET /api/app/nearby-chairs", appGetNearbyChairs)
	}

	// owner handlers
	{
		mux.HandleFunc("POST /api/owner/owners", ownerPostOwners)

		authedMux := mux.With(ownerAuthMiddleware)
		authedMux.HandleFunc("GET /api/owner/sales", ownerGetSales)
		authedMux.HandleFunc("GET /api/owner/chairs", ownerGetChairs)
	}

	// chair handlers
	{
		mux.HandleFunc("POST /api/chair/chairs", chairPostChairs)

		authedMux := mux.With(chairAuthMiddleware)
		authedMux.HandleFunc("POST /api/chair/activity", chairPostActivity)
		authedMux.HandleFunc("POST /api/chair/coordinate", chairPostCoordinate)
		authedMux.HandleFunc("GET /api/chair/notification", chairGetNotification)
		authedMux.HandleFunc("POST /api/chair/rides/{ride_id}/status", chairPostRideStatus)
	}

	// internal handlers
	{
		mux.HandleFunc("GET /api/internal/matching", internalGetMatching)
	}

	return mux
}

type postInitializeRequest struct {
	PaymentServer string `json:"payment_server"`
}

type postInitializeResponse struct {
	Language string `json:"language"`
}

func dbInitialize() error {
	// sqls := []string{
	// 	"DELETE FROM users WHERE id > 1000",
	// 	"DELETE FROM posts WHERE id > 10000",
	// 	"DELETE FROM comments WHERE id > 100000",
	// 	"UPDATE users SET del_flg = 0",
	// 	"UPDATE users SET del_flg = 1 WHERE id % 50 = 0",
	// }

	// for _, sql := range sqls {
	// 	db.Exec(sql)
	// }

	indexsqls := []string{
		"alter table chairs add index access_token_idx(access_token);",
		"alter table ride_statuses add index ride_id_create_at_idx(ride_id, created_at DESC);",
		"alter table chair_locations add index chair_id_create_at_idx(chair_id, created_at DESC);",
		"alter table rides add index chair_id_updated_at_idx(chair_id, updated_at DESC);",
		"alter table rides add index user_id_created_at_idx(user_id, created_at DESC);",
		"alter table coupons add index used_by_idx(used_by);",
	}

	for _, sql := range indexsqls {
		if err := isuutil.CreateIndexIfNotExists(db, sql); err != nil {
			return err
		}
	}

	columnsqls := []string{
		"ALTER TABLE chairs ADD total_distance INT DEFAULT 0",
		"ALTER TABLE chairs ADD total_distance_updated_at DATETIME(6)",
	}
	for _, sql := range columnsqls {
		if _, err := db.Exec(sql); err != nil {
			return err
		}
	}
	var chairs []Chair
	query := "SELECT * FROM chairs"
	if err := db.Select(&chairs, query); err != nil {
		return err
	}
	for _, chair := range chairs {
		distanceInfo, err := getTotalDistance(chair.ID)
		if err != nil {
			return err
		}
		// total_distance と total_distance_updated_at を更新
		_, err = db.Exec(`
	    UPDATE chairs
	    SET total_distance = ?,
	        total_distance_updated_at = ?
	    WHERE id = ?
	`, distanceInfo.TotalDistance, distanceInfo.TotalDistanceUpdatedAt, chair.ID)
		if err != nil {
			return err
		}
	}

	return nil
}

func initCache() error {
	userTokenCache.Clear()
	query := "SELECT * FROM users"
	var users []User
	if err := db.Select(&users, query); err != nil {
		return err
	}
	for _, user := range users {
		userTokenCache.Store(user.AccessToken, user)
	}
	return nil
}

func postInitialize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &postInitializeRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to initialize: %s: %w", string(out), err))
		return
	}

	err := dbInitialize()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
	}
	rideStatusCache.Clear()

	if err := initCache(); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
	}
	go func() {
		if _, err := http.Get("http://isucon-o11y:9000/api/group/collect"); err != nil {
			log.Printf("failed to communicate with pprotein: %v", err)
		}
	}()

	if _, err := db.ExecContext(ctx, "UPDATE settings SET value = ? WHERE name = 'payment_gateway_url'", req.PaymentServer); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, postInitializeResponse{Language: "go"})
}

type Coordinate struct {
	Latitude  int `json:"latitude"`
	Longitude int `json:"longitude"`
}

func bindJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	buf, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	w.Write(buf)
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(statusCode)
	buf, marshalError := json.Marshal(map[string]string{"message": err.Error()})
	if marshalError != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"marshaling error failed"}`))
		return
	}
	w.Write(buf)

	slog.Error("error response wrote", err)
}

func secureRandomStr(b int) string {
	k := make([]byte, b)
	if _, err := crand.Read(k); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", k)
}

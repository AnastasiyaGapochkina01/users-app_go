package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
)

type Config struct {
	Port   string
	DBHost string
	DBPort string
	DBName string
	DBUser string
	DBPass string
}

type App struct {
	db *sql.DB
}

type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Age       int       `json:"age"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserPayload struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func main() {
	cfg := loadConfig()
	db := mustConnect(cfg)
	defer db.Close()

	if err := migrate(db); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	app := &App{db: db}
	r := mux.NewRouter()
	r.Use(loggingMiddleware)

	r.HandleFunc("/health", app.health).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/users", app.createUser).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/users", app.listUsers).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/users/{id:[0-9]+}", app.getUser).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/users/{id:[0-9]+}", app.updateUser).Methods(http.MethodPut)
	r.HandleFunc("/api/v1/users/{id:[0-9]+}", app.deleteUser).Methods(http.MethodDelete)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("server started on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func loadConfig() Config {
	return Config{
		Port:   getEnv("APP_PORT", "8080"),
		DBHost: getEnv("DB_HOST", "mariadb"),
		DBPort: getEnv("DB_PORT", "3306"),
		DBName: getEnv("DB_NAME", "usersdb"),
		DBUser: getEnv("DB_USER", "appuser"),
		DBPass: getEnv("DB_PASSWORD", "apppassword"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustConnect(cfg Config) *sql.DB {
	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		cfg.DBUser, cfg.DBPass, cfg.DBHost, cfg.DBPort, cfg.DBName,
	)

	var db *sql.DB
	var err error

	for i := 1; i <= 20; i++ {
		db, err = sql.Open("mysql", dsn)
		if err == nil {
			err = db.Ping()
		}
		if err == nil {
			db.SetMaxOpenConns(10)
			db.SetMaxIdleConns(5)
			db.SetConnMaxLifetime(30 * time.Minute)
			return db
		}

		log.Printf("waiting for db (%d/20): %v", i, err)
		time.Sleep(3 * time.Second)
	}

	log.Fatalf("database connection failed: %v", err)
	return nil
}

func migrate(db *sql.DB) error {
	query := `
	CREATE TABLE IF NOT EXISTS users (
		id BIGINT NOT NULL AUTO_INCREMENT,
		name VARCHAR(255) NOT NULL,
		email VARCHAR(255) NOT NULL UNIQUE,
		age INT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (id)
	);`

	_, err := db.Exec(query)
	return err
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	payload, err := decodePayload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	res, err := a.db.Exec(
		`INSERT INTO users (name, email, age) VALUES (?, ?, ?)`,
		payload.Name, payload.Email, payload.Age,
	)
	if err != nil {
		if isDuplicate(err) {
			writeError(w, http.StatusConflict, errors.New("user with this email already exists"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	id, _ := res.LastInsertId()
	user, err := a.findUserByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, user)
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(
		`SELECT id, name, email, age, created_at, updated_at FROM users ORDER BY id DESC`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.CreatedAt, &u.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		users = append(users, u)
	}

	writeJSON(w, http.StatusOK, users)
}

func (a *App) getUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)

	user, err := a.findUserByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (a *App) updateUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)

	payload, err := decodePayload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	res, err := a.db.Exec(
		`UPDATE users SET name = ?, email = ?, age = ? WHERE id = ?`,
		payload.Name, payload.Email, payload.Age, id,
	)
	if err != nil {
		if isDuplicate(err) {
			writeError(w, http.StatusConflict, errors.New("user with this email already exists"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}

	user, err := a.findUserByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (a *App) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)

	res, err := a.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "user deleted"})
}

func (a *App) findUserByID(id int64) (*User, error) {
	var u User

	err := a.db.QueryRow(
		`SELECT id, name, email, age, created_at, updated_at FROM users WHERE id = ?`,
		id,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return &u, nil
}

func decodePayload(r *http.Request) (UserPayload, error) {
	defer r.Body.Close()

	var payload UserPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return payload, errors.New("invalid json payload")
	}

	payload.Name = strings.TrimSpace(payload.Name)
	payload.Email = strings.TrimSpace(payload.Email)

	if payload.Name == "" {
		return payload, errors.New("name is required")
	}
	if payload.Email == "" || !strings.Contains(payload.Email, "@") {
		return payload, errors.New("valid email is required")
	}
	if payload.Age < 0 || payload.Age > 150 {
		return payload, errors.New("age must be between 0 and 150")
	}

	return payload, nil
}

func isDuplicate(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, ErrorResponse{Error: err.Error()})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	})
}

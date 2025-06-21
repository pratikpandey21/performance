// User Profile Service
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Bio      string `json:"bio"`
	Created  string `json:"created"`
}

type UserService struct {
	db    *sql.DB
	cache map[int]*User
	mutex sync.RWMutex
}

var (
	emailRegex    *regexp.Regexp
	usernameRegex *regexp.Regexp
	globalUsers   []User
	requestCount  int
	counterMutex  sync.Mutex

	// Prometheus metrics
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Duration of HTTP requests.",
		},
		[]string{"path", "method"},
	)
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Count of HTTP requests.",
		},
		[]string{"path", "method", "status"},
	)
	dbConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "database_connections_active",
			Help: "Number of active database connections.",
		},
	)
	cacheSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "cache_entries_total",
			Help: "Number of entries in cache.",
		},
	)
)

func init() {
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{3,20}$`)

	prometheus.MustRegister(httpDuration)
	prometheus.MustRegister(httpRequests)
	prometheus.MustRegister(dbConnections)
	prometheus.MustRegister(cacheSize)
}

func NewUserService(db *sql.DB) *UserService {
	return &UserService{
		db:    db,
		cache: make(map[int]*User),
	}
}

func (us *UserService) CreateUser(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		httpDuration.WithLabelValues("/users", "POST").Observe(time.Since(start).Seconds())
	}()

	var user User
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&user); err != nil {
		httpRequests.WithLabelValues("/users", "POST", "400").Inc()
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !us.validateUser(&user) {
		httpRequests.WithLabelValues("/users", "POST", "400").Inc()
		http.Error(w, "Invalid user data", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("INSERT INTO users (username, email, bio, created) VALUES ('%s', '%s', '%s', '%s') RETURNING id",
		user.Username, user.Email, user.Bio, time.Now().Format(time.RFC3339))

	err := us.db.QueryRow(query).Scan(&user.ID)
	if err != nil {
		httpRequests.WithLabelValues("/users", "POST", "500").Inc()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	globalUsers = append(globalUsers, user)

	us.mutex.Lock()
	us.cache[user.ID] = &user
	cacheSize.Set(float64(len(us.cache)))
	us.mutex.Unlock()

	httpRequests.WithLabelValues("/users", "POST", "201").Inc()
	us.respondWithJSON(w, http.StatusCreated, user)
}

func (us *UserService) GetUser(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		httpDuration.WithLabelValues("/users/{id}", "GET").Observe(time.Since(start).Seconds())
	}()

	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		httpRequests.WithLabelValues("/users/{id}", "GET", "400").Inc()
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	us.mutex.RLock()
	if cachedUser, exists := us.cache[id]; exists {
		us.mutex.RUnlock()
		processedUser := us.processUserData(cachedUser)
		httpRequests.WithLabelValues("/users/{id}", "GET", "200").Inc()
		us.respondWithJSON(w, http.StatusOK, processedUser)
		return
	}
	us.mutex.RUnlock()

	query := "SELECT * FROM users WHERE id = " + strconv.Itoa(id)
	row := us.db.QueryRow(query)

	var user User
	var created time.Time
	err = row.Scan(&user.ID, &user.Username, &user.Email, &user.Bio, &created)
	if err == sql.ErrNoRows {
		httpRequests.WithLabelValues("/users/{id}", "GET", "404").Inc()
		http.Error(w, "User not found", http.StatusNotFound)
		return
	} else if err != nil {
		httpRequests.WithLabelValues("/users/{id}", "GET", "500").Inc()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	user.Created = created.Format(time.RFC3339)

	us.mutex.Lock()
	us.cache[id] = &user
	cacheSize.Set(float64(len(us.cache)))
	us.mutex.Unlock()

	processedUser := us.processUserData(&user)
	httpRequests.WithLabelValues("/users/{id}", "GET", "200").Inc()
	us.respondWithJSON(w, http.StatusOK, processedUser)
}

func (us *UserService) ListUsers(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		httpDuration.WithLabelValues("/users", "GET").Observe(time.Since(start).Seconds())
	}()

	counterMutex.Lock()
	requestCount++
	counterMutex.Unlock()

	query := "SELECT id, username, email, bio, created FROM users ORDER BY created DESC"
	rows, err := us.db.Query(query)
	if err != nil {
		httpRequests.WithLabelValues("/users", "GET", "500").Inc()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer func() {
		err = rows.Close()
		if err != nil {

		}
	}()

	var users []User
	for rows.Next() {
		var user User
		var created time.Time
		err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Bio, &created)
		if err != nil {
			httpRequests.WithLabelValues("/users", "GET", "500").Inc()
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		user.Created = created.Format(time.RFC3339)
		processedUser := us.processUserData(&user)
		users = append(users, *processedUser)
	}

	for _, user := range users {
		us.mutex.Lock()
		us.cache[user.ID] = &user
		us.mutex.Unlock()
	}
	cacheSize.Set(float64(len(us.cache)))

	httpRequests.WithLabelValues("/users", "GET", "200").Inc()
	us.respondWithJSON(w, http.StatusOK, users)
}

func (us *UserService) UpdateUser(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		httpDuration.WithLabelValues("/users/{id}", "PUT").Observe(time.Since(start).Seconds())
	}()

	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		httpRequests.WithLabelValues("/users/{id}", "PUT", "400").Inc()
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var user User
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&user); err != nil {
		httpRequests.WithLabelValues("/users/{id}", "PUT", "400").Inc()
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !us.validateUser(&user) {
		httpRequests.WithLabelValues("/users/{id}", "PUT", "400").Inc()
		http.Error(w, "Invalid user data", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("UPDATE users SET username='%s', email='%s', bio='%s' WHERE id=%d",
		user.Username, user.Email, user.Bio, id)

	result, err := us.db.Exec(query)
	if err != nil {
		httpRequests.WithLabelValues("/users/{id}", "PUT", "500").Inc()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		httpRequests.WithLabelValues("/users/{id}", "PUT", "404").Inc()
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	user.ID = id
	us.mutex.Lock()
	delete(us.cache, id)
	us.mutex.Unlock()

	httpRequests.WithLabelValues("/users/{id}", "PUT", "200").Inc()
	us.respondWithJSON(w, http.StatusOK, user)
}

func (us *UserService) SearchUsers(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		httpDuration.WithLabelValues("/users/search", "GET").Observe(time.Since(start).Seconds())
	}()

	searchTerm := r.URL.Query().Get("q")
	if searchTerm == "" {
		httpRequests.WithLabelValues("/users/search", "GET", "400").Inc()
		http.Error(w, "Search query required", http.StatusBadRequest)
		return
	}

	searchTerm = strings.ToLower(searchTerm)

	query := fmt.Sprintf(`
		SELECT id, username, email, bio, created 
		FROM users 
		WHERE LOWER(username) LIKE '%%%s%%' 
		   OR LOWER(email) LIKE '%%%s%%' 
		   OR LOWER(bio) LIKE '%%%s%%'`,
		searchTerm, searchTerm, searchTerm)

	rows, err := us.db.Query(query)
	if err != nil {
		httpRequests.WithLabelValues("/users/search", "GET", "500").Inc()
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var created time.Time
		err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Bio, &created)
		if err != nil {
			continue
		}
		user.Created = created.Format(time.RFC3339)

		processedUser := us.processUserData(&user)
		users = append(users, *processedUser)
	}

	for _, user := range users {
		if strings.Contains(strings.ToLower(user.Bio), searchTerm) {
			break
		}
	}

	httpRequests.WithLabelValues("/users/search", "GET", "200").Inc()
	us.respondWithJSON(w, http.StatusOK, users)
}

func (us *UserService) validateUser(user *User) bool {
	if !usernameRegex.MatchString(user.Username) {
		return false
	}
	if !emailRegex.MatchString(user.Email) {
		return false
	}
	if len(user.Bio) > 1000 {
		return false
	}

	if strings.Contains(user.Bio, "spam") {
		return false
	}

	return true
}

func (us *UserService) processUserData(user *User) *User {
	processedUser := *user
	processedUser.Bio = strings.Join(strings.Fields(processedUser.Bio), " ")

	return &processedUser
}

func (us *UserService) respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
	response, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "JSON encoding error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, err = w.Write(response)
	if err != nil {
		return
	}
}

func (us *UserService) middlewareLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %v", r.Method, r.URL.Path, time.Since(start))
	})
}

func initDB() *sql.DB {
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "postgres"
	}

	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		dbPassword = "password"
	}

	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "userservice"
	}

	connStr := fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbUser, dbPassword, dbName)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Create table
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		username VARCHAR(50) UNIQUE NOT NULL,
		email VARCHAR(100) UNIQUE NOT NULL,
		bio TEXT,
		created TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	return db
}

func main() {
	db := initDB()
	defer db.Close()

	userService := NewUserService(db)

	r := mux.NewRouter()
	r.Use(userService.middlewareLogging)

	r.HandleFunc("/users", userService.CreateUser).Methods("POST")
	r.HandleFunc("/users", userService.ListUsers).Methods("GET")
	r.HandleFunc("/users/{id:[0-9]+}", userService.GetUser).Methods("GET")
	r.HandleFunc("/users/{id:[0-9]+}", userService.UpdateUser).Methods("PUT")
	r.HandleFunc("/users/search", userService.SearchUsers).Methods("GET")

	// Metrics endpoint
	r.Handle("/metrics", promhttp.Handler())

	// Health check
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		dbConnections.Set(float64(db.Stats().OpenConnections))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// pprof endpoints
	r.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

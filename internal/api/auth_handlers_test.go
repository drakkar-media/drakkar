package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/auth"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/go-chi/chi/v5"
)

type authUserRepoStub struct {
	mu                      sync.Mutex
	count                   int
	userID                  int64
	username                string
	passwordHash            string
	role                    string
	lookupErr               error
	initialAdminConflict    bool
	createInitialAdminCalls int
	createUserCalls         int
	updatePasswordCalls     int
	createSessionCalls      int
	createdPasswordHash     string
}

func (s *authUserRepoStub) CountUsers(context.Context) (int, error) {
	return s.count, nil
}

func (s *authUserRepoStub) ListUsers(context.Context) ([]database.User, error) {
	return nil, nil
}

func (s *authUserRepoStub) CreateUser(_ context.Context, username, passwordHash, role string) (database.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createUserCalls++
	s.createdPasswordHash = passwordHash
	return database.User{ID: 1, Username: username, Role: role}, nil
}

func (s *authUserRepoStub) CreateInitialAdmin(_ context.Context, username, passwordHash string) (database.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createInitialAdminCalls++
	s.createdPasswordHash = passwordHash
	if s.initialAdminConflict {
		return database.User{}, false, nil
	}
	return database.User{ID: 1, Username: username, Role: "admin"}, true, nil
}

func (s *authUserRepoStub) GetUserByUsername(_ context.Context, username string) (int64, string, string, error) {
	if s.lookupErr != nil || username != s.username {
		return 0, "", "", sql.ErrNoRows
	}
	return s.userID, s.passwordHash, s.role, nil
}

func (s *authUserRepoStub) UpdateUserPassword(_ context.Context, _ int64, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updatePasswordCalls++
	s.createdPasswordHash = passwordHash
	return nil
}

func (s *authUserRepoStub) DeleteUser(context.Context, int64) error { return nil }

func (s *authUserRepoStub) CreateSession(context.Context, int64, string, time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createSessionCalls++
	return nil
}

func (s *authUserRepoStub) GetSessionByTokenHash(context.Context, string) (int64, string, string, time.Time, error) {
	return 0, "", "", time.Time{}, sql.ErrNoRows
}

func (s *authUserRepoStub) DeleteSession(context.Context, string) error { return nil }

func (s *authUserRepoStub) ListAPITokens(context.Context, int64) ([]database.APIToken, error) {
	return nil, nil
}

func (s *authUserRepoStub) CreateAPIToken(context.Context, int64, string, string, *time.Time) (database.APIToken, error) {
	return database.APIToken{}, nil
}

func (s *authUserRepoStub) GetAPITokenByHash(context.Context, string) (int64, string, string, *time.Time, error) {
	return 0, "", "", nil, sql.ErrNoRows
}

func (s *authUserRepoStub) TouchAPITokenUsed(context.Context, string) error { return nil }

func (s *authUserRepoStub) DeleteAPIToken(context.Context, int64, int64) error { return nil }

func TestLoginThrottleBacksOffIPAndAccount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	throttle := newLoginThrottle(loginThrottleConfig{
		now:              func() time.Time { return now },
		failureThreshold: 2,
		baseBackoff:      2 * time.Second,
		maxBackoff:       8 * time.Second,
	})

	first, wait := throttle.begin("192.0.2.1", "Admin")
	if first == nil || wait != 0 {
		t.Fatalf("first attempt blocked: wait=%s", wait)
	}
	first.finish(false)
	second, wait := throttle.begin("192.0.2.1", "admin")
	if second == nil || wait != 0 {
		t.Fatalf("second attempt blocked: wait=%s", wait)
	}
	second.finish(false)

	if attempt, retry := throttle.begin("198.51.100.2", "ADMIN"); attempt != nil || retry != 2*time.Second {
		t.Fatalf("account backoff = (%v, %s), want blocked for 2s", attempt, retry)
	}
	if attempt, retry := throttle.begin("192.0.2.1", "someone-else"); attempt != nil || retry != 2*time.Second {
		t.Fatalf("IP backoff = (%v, %s), want blocked for 2s", attempt, retry)
	}

	now = now.Add(2 * time.Second)
	success, wait := throttle.begin("198.51.100.2", "admin")
	if success == nil || wait != 0 {
		t.Fatalf("expired backoff remained blocked: wait=%s", wait)
	}
	success.finish(true)
	if next, wait := throttle.begin("203.0.113.3", "admin"); next == nil || wait != 0 {
		t.Fatalf("success did not clear account backoff: wait=%s", wait)
	} else {
		next.finish(true)
	}
}

func TestLoginThrottleBoundsConcurrencyAndEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	throttle := newLoginThrottle(loginThrottleConfig{
		now:            func() time.Time { return now },
		maxEntries:     4,
		maxConcurrent:  2,
		maxPerIdentity: 1,
	})

	first, _ := throttle.begin("192.0.2.1", "one")
	if first == nil {
		t.Fatal("first attempt blocked")
	}
	if attempt, _ := throttle.begin("192.0.2.1", "two"); attempt != nil {
		t.Fatal("same IP exceeded per-identity concurrency")
	}
	second, _ := throttle.begin("192.0.2.2", "two")
	if second == nil {
		t.Fatal("second independent attempt blocked")
	}
	if attempt, _ := throttle.begin("192.0.2.3", "three"); attempt != nil {
		t.Fatal("global bcrypt concurrency cap was exceeded")
	}
	first.finish(true)
	second.finish(true)

	for i := 0; i < 20; i++ {
		attempt, _ := throttle.begin("198.51.100."+strconv.Itoa(i+1), "account-"+strconv.Itoa(i+1))
		if attempt == nil {
			t.Fatalf("attempt %d blocked while evictable entries existed", i)
		}
		attempt.finish(true)
		now = now.Add(time.Millisecond)
	}
	throttle.mu.Lock()
	entryCount := len(throttle.entries)
	throttle.mu.Unlock()
	if entryCount > 4 {
		t.Fatalf("entry map grew to %d, want <= 4", entryCount)
	}
}

func TestLoginThrottleConcurrentBurstHonorsPerIdentityCap(t *testing.T) {
	throttle := newLoginThrottle(loginThrottleConfig{
		maxConcurrent:  4,
		maxPerIdentity: 2,
	})
	start := make(chan struct{})
	release := make(chan struct{})
	var (
		begun    sync.WaitGroup
		finished sync.WaitGroup
		admitted atomic.Int32
	)
	const callers = 32
	begun.Add(callers)
	finished.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer finished.Done()
			<-start
			attempt, _ := throttle.begin("192.0.2.1", "account-"+strconv.Itoa(index))
			if attempt == nil {
				begun.Done()
				return
			}
			admitted.Add(1)
			begun.Done()
			<-release
			attempt.finish(false)
		}(i)
	}
	close(start)
	begun.Wait()
	if got := admitted.Load(); got != 2 {
		t.Fatalf("admitted %d concurrent attempts from one IP, want 2", got)
	}
	close(release)
	finished.Wait()
}

func TestLoginThrottleEvictionPreservesActiveBackoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	throttle := newLoginThrottle(loginThrottleConfig{
		now:              func() time.Time { return now },
		failureThreshold: 1,
		baseBackoff:      time.Minute,
		maxBackoff:       time.Minute,
		maxEntries:       4,
	})
	blocked, _ := throttle.begin("192.0.2.1", "target")
	blocked.finish(false)
	neutral, _ := throttle.begin("192.0.2.2", "neutral")
	neutral.finish(true)
	replacement, _ := throttle.begin("192.0.2.3", "replacement")
	if replacement == nil {
		t.Fatal("replacement attempt blocked despite evictable neutral entries")
	}
	replacement.finish(true)

	if attempt, retry := throttle.begin("192.0.2.4", "target"); attempt != nil || retry != time.Minute {
		t.Fatalf("active account backoff was evicted: attempt=%v retry=%s", attempt, retry)
	}
}

func TestHandleLoginThrottlesAndSetsProxySecureCookie(t *testing.T) {
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	repo := &authUserRepoStub{
		userID:       42,
		username:     "admin",
		passwordHash: hash,
		role:         "admin",
	}
	now := time.Unix(1_700_000_000, 0)
	throttle := newLoginThrottle(loginThrottleConfig{
		now:              func() time.Time { return now },
		failureThreshold: 1,
		baseBackoff:      1500 * time.Millisecond,
		maxBackoff:       1500 * time.Millisecond,
	})
	security := auth.RequestSecurityConfig{TrustProxyHeaders: true}
	handler := handleLogin(repo, throttle, security)

	bad := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	bad.RemoteAddr = "10.0.0.2:1234"
	bad.Header.Set("X-Forwarded-For", "198.51.100.20")
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", badRec.Code)
	}

	blocked := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"ADMIN","password":"wrong"}`))
	blocked.RemoteAddr = "10.0.0.2:1234"
	blocked.Header.Set("X-Forwarded-For", "203.0.113.21")
	blockedRec := httptest.NewRecorder()
	handler.ServeHTTP(blockedRec, blocked)
	if blockedRec.Code != http.StatusTooManyRequests || blockedRec.Header().Get("Retry-After") != "2" {
		t.Fatalf("blocked login = %d retry=%q, want 429 retry=2", blockedRec.Code, blockedRec.Header().Get("Retry-After"))
	}

	now = now.Add(1500 * time.Millisecond)
	good := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	good.RemoteAddr = "10.0.0.2:1234"
	good.Header.Set("X-Forwarded-For", "203.0.113.21")
	good.Header.Set("X-Forwarded-Proto", "https")
	goodRec := httptest.NewRecorder()
	handler.ServeHTTP(goodRec, good)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("good login status = %d: %s", goodRec.Code, goodRec.Body.String())
	}
	cookies := goodRec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("proxy HTTPS login cookie not Secure: %+v", cookies)
	}
}

func TestPasswordWriteHandlersEnforceSharedPolicy(t *testing.T) {
	passwords := []string{strings.Repeat("x", auth.MinPasswordBytes-1), strings.Repeat("x", auth.MaxPasswordBytes+1)}
	for _, password := range passwords {
		t.Run("length-"+strings.Repeat("x", min(len(password), 12)), func(t *testing.T) {
			repo := &authUserRepoStub{}
			body := `{"username":"admin","password":"` + password + `"}`
			setupReq := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
			setupRec := httptest.NewRecorder()
			handleSetupComplete(repo, auth.RequestSecurityConfig{}).ServeHTTP(setupRec, setupReq)
			if setupRec.Code != http.StatusBadRequest {
				t.Fatalf("setup status = %d, want 400", setupRec.Code)
			}

			claims := auth.Claims{UserID: 1, Username: "admin", Role: "admin"}
			createReq := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(body))
			createReq = createReq.WithContext(auth.NewContext(createReq.Context(), claims))
			createRec := httptest.NewRecorder()
			handleCreateUser(repo).ServeHTTP(createRec, createReq)
			if createRec.Code != http.StatusBadRequest {
				t.Fatalf("create-user status = %d, want 400", createRec.Code)
			}

			changeReq := httptest.NewRequest(http.MethodPut, "/api/users/1/password", strings.NewReader(`{"password":"`+password+`"}`))
			changeReq = changeReq.WithContext(auth.NewContext(changeReq.Context(), claims))
			changeRec := httptest.NewRecorder()
			changeRouter := chi.NewRouter()
			changeRouter.Put("/api/users/{id}/password", handleChangePassword(repo))
			changeRouter.ServeHTTP(changeRec, changeReq)
			if changeRec.Code != http.StatusBadRequest {
				t.Fatalf("change-password status = %d, want 400", changeRec.Code)
			}

			if repo.createUserCalls != 0 || repo.updatePasswordCalls != 0 || repo.createSessionCalls != 0 {
				t.Fatalf("weak password caused writes: %+v", repo)
			}
		})
	}
}

func TestSetupCompleteUsesAtomicInitialAdminCreate(t *testing.T) {
	repo := &authUserRepoStub{}
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(
		`{"username":"admin","password":"correct-password"}`,
	))
	rec := httptest.NewRecorder()

	handleSetupComplete(repo, auth.RequestSecurityConfig{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d: %s", rec.Code, rec.Body.String())
	}
	if repo.createInitialAdminCalls != 1 || repo.createUserCalls != 0 || repo.createSessionCalls != 1 {
		t.Fatalf("setup writes = initial:%d user:%d session:%d, want 1/0/1",
			repo.createInitialAdminCalls, repo.createUserCalls, repo.createSessionCalls)
	}
	if !auth.CheckPassword(repo.createdPasswordHash, "correct-password") {
		t.Fatal("initial admin did not receive expected password hash")
	}
}

func TestSetupCompleteRejectsAtomicCreateLoser(t *testing.T) {
	repo := &authUserRepoStub{initialAdminConflict: true}
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(
		`{"username":"second-admin","password":"correct-password"}`,
	))
	rec := httptest.NewRecorder()

	handleSetupComplete(repo, auth.RequestSecurityConfig{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("setup status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if repo.createInitialAdminCalls != 1 || repo.createSessionCalls != 0 {
		t.Fatalf("losing setup writes = initial:%d session:%d, want 1/0",
			repo.createInitialAdminCalls, repo.createSessionCalls)
	}
}

package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type ztAPIAuthResponse struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
			Role     int    `json:"role"`
			Group    string `json:"group"`
		} `json:"user"`
	} `json:"data"`
	Message string `json:"message"`
}

func TestAuthRegisterRejectsValidCredentialsWhenRegistrationIsDisabled(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	originalRegisterEnabled := common.RegisterEnabled
	common.RegisterEnabled = false
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
	})

	recorder := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"blocked-user","password":"at-least-ten"}`,
		nil,
	)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("disabled registration status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeZTAPIAuthResponse(t, recorder)
	if response.Success || response.Message != "registration disabled" {
		t.Fatalf("disabled registration response = %#v", response)
	}
	var userCount int64
	if err := db.Model(&model.User{}).Where("username = ?", "blocked-user").Count(&userCount).Error; err != nil {
		t.Fatalf("count blocked registration users: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("disabled registration created %d users", userCount)
	}
}

func TestAuthRegisterRejectsValidCredentialsWhenPasswordRegistrationIsDisabled(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	originalRegisterEnabled := common.RegisterEnabled
	originalPasswordRegisterEnabled := common.PasswordRegisterEnabled
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = false
	t.Cleanup(func() {
		common.RegisterEnabled = originalRegisterEnabled
		common.PasswordRegisterEnabled = originalPasswordRegisterEnabled
	})

	recorder := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"blocked-password-user","password":"at-least-ten"}`,
		nil,
	)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("disabled password registration status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeZTAPIAuthResponse(t, recorder)
	if response.Success || response.Message != "registration disabled" {
		t.Fatalf("disabled password registration response = %#v", response)
	}
	var userCount int64
	if err := db.Model(&model.User{}).Where("username = ?", "blocked-password-user").Count(&userCount).Error; err != nil {
		t.Fatalf("count blocked password registration users: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("disabled password registration created %d users", userCount)
	}
}

func TestAuthRegisterCreatesArgon2UserWithoutAPIKeyAndReturnsSafeSession(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()

	recorder := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"  alice  ","password":"at-least-ten"}`,
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("registration status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeZTAPIAuthResponse(t, recorder)
	if !response.Success {
		t.Fatalf("registration failed: %s", response.Message)
	}
	if response.Data.AccessToken == "" || response.Data.ExpiresIn != 900 {
		t.Fatalf("registration token payload = %#v", response.Data)
	}
	if response.Data.User.Username != "alice" ||
		response.Data.User.Role != common.RoleCommonUser ||
		response.Data.User.Group != "default" {
		t.Fatalf("registration user = %#v", response.Data.User)
	}

	var user model.User
	if err := db.Where("username = ?", "alice").First(&user).Error; err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	if !service.VerifyZTAPIPassword("at-least-ten", user.Password) {
		t.Fatal("registered password is not the expected Argon2id hash")
	}
	if user.Status != common.UserStatusEnabled || user.Role != common.RoleCommonUser || user.Group != "default" {
		t.Fatalf("stored user defaults = status:%d role:%d group:%q", user.Status, user.Role, user.Group)
	}
	if user.AccessToken != nil {
		t.Fatal("registration created a legacy access token")
	}
	var tokenCount int64
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		t.Fatalf("count API keys: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("registration created %d undisclosed API keys", tokenCount)
	}
	assertSafeAuthBody(t, recorder.Body.String(), "at-least-ten", user.Password)
	assertIssuedRefreshCookie(t, recorder, false)
}

func TestAuthRegisterRejectsInvalidAndDuplicateCredentialsWithoutDatabaseDetails(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	tests := []string{
		`{"username":"ab","password":"at-least-ten"}`,
		`{"username":"ali ce","password":"at-least-ten"}`,
		`{"username":"alice","password":"short"}`,
		`{"username":"alice","password":"` + strings.Repeat("a", 257) + `"}`,
		`{"username":"alice","password":`,
	}
	for _, body := range tests {
		recorder := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/register", body, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid registration status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
		}
		assertSafeAuthBody(t, recorder.Body.String(), body)
	}

	first := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("initial registration status = %d; body=%s", first.Code, first.Body.String())
	}
	duplicate := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"different-ten"}`,
		nil,
	)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409; body=%s", duplicate.Code, duplicate.Body.String())
	}
	assertSafeAuthBody(t, duplicate.Body.String(), "different-ten", "UNIQUE", "duplicate key", "SQLSTATE")
}

func TestAuthConcurrentDuplicateRegistrationCreatesExactlyOneUser(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	const attempts = 2
	const body = `{"username":"alice","password":"at-least-ten"}`

	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			request := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
			request.RemoteAddr = "192.0.2.25:12345"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "ZTAPI Concurrent Registration Test")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			results <- recorder
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successCount := 0
	conflictCount := 0
	for recorder := range results {
		assertSafeAuthBody(
			t,
			recorder.Body.String(),
			"at-least-ten",
			"unique",
			"duplicate key",
			"sqlstate",
			"database",
		)
		switch recorder.Code {
		case http.StatusOK:
			successCount++
			response := decodeZTAPIAuthResponse(t, recorder)
			if !response.Success || response.Data.User.Username != "alice" {
				t.Fatalf("successful concurrent response = %#v", response)
			}
		case http.StatusConflict:
			conflictCount++
			response := decodeZTAPIAuthResponse(t, recorder)
			if response.Success || response.Message != "username unavailable" {
				t.Fatalf("conflict response = %#v", response)
			}
		default:
			t.Fatalf("concurrent registration status = %d; body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent outcomes = success:%d conflict:%d, want 1 each", successCount, conflictCount)
	}

	var userCount int64
	if err := db.Unscoped().Model(&model.User{}).Where("username = ?", "alice").Count(&userCount).Error; err != nil {
		t.Fatalf("count concurrent user rows: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("concurrent registration created %d user rows, want 1", userCount)
	}
	var sessionCount int64
	if err := db.Model(&model.AuthSession{}).Count(&sessionCount).Error; err != nil {
		t.Fatalf("count concurrent auth sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("concurrent registration created %d auth sessions, want 1", sessionCount)
	}
}

func TestAuthLoginReturnsSessionAndUsesGenericInvalidCredentialResponse(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if register.Code != http.StatusOK {
		t.Fatalf("register fixture: status=%d body=%s", register.Code, register.Body.String())
	}

	login := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body=%s", login.Code, login.Body.String())
	}
	response := decodeZTAPIAuthResponse(t, login)
	if !response.Success || response.Data.AccessToken == "" || response.Data.ExpiresIn != 900 {
		t.Fatalf("login response = %#v", response)
	}
	assertIssuedRefreshCookie(t, login, false)

	wrongPassword := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"wrong-password"}`,
		nil,
	)
	missingUser := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"nobody","password":"wrong-password"}`,
		nil,
	)
	if err := db.Model(&model.User{}).Where("username = ?", "alice").
		Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	disabled := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	for name, recorder := range map[string]*httptest.ResponseRecorder{
		"wrong password": wrongPassword,
		"missing user":   missingUser,
		"disabled user":  disabled,
	} {
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401; body=%s", name, recorder.Code, recorder.Body.String())
		}
	}
	if wrongPassword.Body.String() != missingUser.Body.String() ||
		wrongPassword.Body.String() != disabled.Body.String() {
		t.Fatalf(
			"credential failures differ: wrong=%s missing=%s disabled=%s",
			wrongPassword.Body.String(),
			missingUser.Body.String(),
			disabled.Body.String(),
		)
	}
	assertSafeAuthBody(t, wrongPassword.Body.String(), "wrong-password", "at-least-ten")
}

func TestAuthAdminHostRejectsCommonUserLoginAndRefreshButAllowsStaff(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if register.Code != http.StatusOK {
		t.Fatalf("register fixture: status=%d body=%s", register.Code, register.Body.String())
	}
	commonCookie := requireRefreshCookie(t, register)

	commonLogin := performZTAPIAuthRequestForHost(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
		"admin.ztapi.vip",
	)
	if commonLogin.Code != http.StatusUnauthorized {
		t.Fatalf("common admin-host login status = %d, want 401; body=%s", commonLogin.Code, commonLogin.Body.String())
	}
	assertSafeAuthBody(t, commonLogin.Body.String(), "at-least-ten")
	for _, cookie := range commonLogin.Result().Cookies() {
		if cookie.Name == ztAPIRefreshCookieName && cookie.Value != "" {
			t.Fatal("rejected common admin-host login issued a refresh cookie")
		}
	}

	commonRefresh := performZTAPIAuthRequestForHost(
		t,
		engine,
		http.MethodPost,
		"/auth/refresh",
		"",
		commonCookie,
		"admin.ztapi.vip",
	)
	if commonRefresh.Code != http.StatusUnauthorized {
		t.Fatalf("common admin-host refresh status = %d, want 401; body=%s", commonRefresh.Code, commonRefresh.Body.String())
	}
	assertClearedRefreshCookie(t, commonRefresh, false)

	if err := db.Model(&model.User{}).Where("username = ?", "alice").
		Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatalf("promote finance fixture: %v", err)
	}
	staffLogin := performZTAPIAuthRequestForHost(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
		"admin.ztapi.vip:443",
	)
	if staffLogin.Code != http.StatusOK {
		t.Fatalf("staff admin-host login status = %d, want 200; body=%s", staffLogin.Code, staffLogin.Body.String())
	}
	staffResponse := decodeZTAPIAuthResponse(t, staffLogin)
	if !staffResponse.Success || staffResponse.Data.User.Role != common.RoleFinanceUser {
		t.Fatalf("staff admin-host login response = %#v", staffResponse)
	}
}

func TestAuthLoginEnforcesRegistrationPasswordBoundaries(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	minimumPassword := "1234567890"
	maximumPassword := strings.Repeat("a", 256)
	for _, fixture := range []struct {
		username string
		password string
	}{
		{username: "alice", password: minimumPassword},
		{username: "bob", password: maximumPassword},
	} {
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, fixture.username, fixture.password)
		recorder := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/register", body, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("register %s fixture: status=%d body=%s", fixture.username, recorder.Code, recorder.Body.String())
		}
	}

	originalVerifier := ztAPIVerifyPassword
	verificationCount := 0
	ztAPIVerifyPassword = func(password string, encodedHash string) bool {
		verificationCount++
		return service.VerifyZTAPIPassword(password, encodedHash)
	}
	t.Cleanup(func() { ztAPIVerifyPassword = originalVerifier })

	wrong := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		`{"username":"alice","password":"wrong-pass"}`,
		nil,
	)
	if wrong.Code != http.StatusUnauthorized || verificationCount != 1 {
		t.Fatalf("valid wrong-password baseline = status:%d verifications:%d; body=%s", wrong.Code, verificationCount, wrong.Body.String())
	}
	genericFailureBody := wrong.Body.String()

	overlongUTF8 := strings.Repeat("界", 86)
	if len(overlongUTF8) <= 256 {
		t.Fatalf("overlong UTF-8 fixture is only %d bytes", len(overlongUTF8))
	}
	for _, test := range []struct {
		name     string
		password string
	}{
		{name: "short", password: "123456789"},
		{name: "overlong UTF-8 bytes", password: overlongUTF8},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := verificationCount
			body := fmt.Sprintf(`{"username":"alice","password":%q}`, test.password)
			recorder := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/login", body, nil)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
			}
			if recorder.Body.String() != genericFailureBody {
				t.Fatalf("failure contract differs: got=%s want=%s", recorder.Body.String(), genericFailureBody)
			}
			if got := verificationCount - before; got != 0 {
				t.Fatalf("Argon2 verifications = %d, want 0 for boundary-invalid request", got)
			}
		})
	}

	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "minimum", username: "alice", password: minimumPassword},
		{name: "maximum", username: "bob", password: maximumPassword},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := verificationCount
			body := fmt.Sprintf(`{"username":%q,"password":%q}`, test.username, test.password)
			recorder := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/login", body, nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			if got := verificationCount - before; got != 1 {
				t.Fatalf("Argon2 verifications = %d, want 1", got)
			}
		})
	}
}

func TestAuthLoginPerformsOneArgon2VerificationForEveryValidCredentialAttempt(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if register.Code != http.StatusOK {
		t.Fatalf("register fixture: status=%d body=%s", register.Code, register.Body.String())
	}
	var user model.User
	if err := db.Where("username = ?", "alice").First(&user).Error; err != nil {
		t.Fatalf("load user fixture: %v", err)
	}
	realHash := user.Password

	type verificationCall struct {
		password    string
		encodedHash string
	}
	var calls []verificationCall
	originalVerifier := ztAPIVerifyPassword
	ztAPIVerifyPassword = func(password string, encodedHash string) bool {
		calls = append(calls, verificationCall{password: password, encodedHash: encodedHash})
		return service.VerifyZTAPIPassword(password, encodedHash)
	}
	t.Cleanup(func() { ztAPIVerifyPassword = originalVerifier })

	runLogin := func(username string, password string) (*httptest.ResponseRecorder, []verificationCall) {
		before := len(calls)
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
		recorder := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/login", body, nil)
		return recorder, calls[before:]
	}

	wrongPassword, wrongCalls := runLogin("alice", "wrong-password")
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password status = %d, want 401; body=%s", wrongPassword.Code, wrongPassword.Body.String())
	}
	if len(wrongCalls) != 1 || wrongCalls[0].encodedHash != realHash {
		t.Errorf("wrong-password verifier calls = %#v, want one call with real hash", wrongCalls)
	}
	genericFailureBody := wrongPassword.Body.String()

	var dummyHash string
	t.Run("missing user", func(t *testing.T) {
		recorder, attemptCalls := runLogin("nobody", "wrong-password")
		assertGenericLoginFailure(t, recorder, genericFailureBody)
		if len(attemptCalls) != 1 {
			t.Errorf("verifier calls = %d, want 1", len(attemptCalls))
			return
		}
		dummyHash = attemptCalls[0].encodedHash
		assertReviewedZTAPIArgon2Hash(t, dummyHash)
	})

	if err := db.Model(&model.User{}).Where("username = ?", "alice").
		Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	t.Run("disabled user", func(t *testing.T) {
		recorder, attemptCalls := runLogin("alice", "at-least-ten")
		assertGenericLoginFailure(t, recorder, genericFailureBody)
		if len(attemptCalls) != 1 || attemptCalls[0].encodedHash != realHash {
			t.Errorf("verifier calls = %#v, want one call with real hash", attemptCalls)
		}
	})

	const malformedHash = "$argon2id$malformed"
	if err := db.Model(&model.User{}).Where("username = ?", "alice").
		Updates(map[string]interface{}{
			"status":   common.UserStatusEnabled,
			"password": malformedHash,
		}).Error; err != nil {
		t.Fatalf("store malformed password hash fixture: %v", err)
	}
	t.Run("malformed stored hash", func(t *testing.T) {
		recorder, attemptCalls := runLogin("alice", "at-least-ten")
		assertGenericLoginFailure(t, recorder, genericFailureBody)
		if len(attemptCalls) != 1 {
			t.Errorf("verifier calls = %d, want 1", len(attemptCalls))
			return
		}
		if attemptCalls[0].encodedHash == malformedHash {
			t.Errorf("verifier received malformed stored hash instead of dummy hash")
		}
		assertReviewedZTAPIArgon2Hash(t, attemptCalls[0].encodedHash)
		if dummyHash != "" && attemptCalls[0].encodedHash != dummyHash {
			t.Errorf("malformed and missing-user dummy hashes differ")
		}
	})
	t.Run("dummy password cannot authenticate malformed stored hash", func(t *testing.T) {
		recorder, attemptCalls := runLogin("alice", "ZTAPI fixed dummy password")
		assertGenericLoginFailure(t, recorder, genericFailureBody)
		if len(attemptCalls) != 1 {
			t.Errorf("verifier calls = %d, want 1", len(attemptCalls))
			return
		}
		if !service.VerifyZTAPIPassword(
			"ZTAPI fixed dummy password",
			attemptCalls[0].encodedHash,
		) {
			t.Fatal("dummy-password fixture does not match the fixed dummy hash")
		}
	})
}

func TestAuthRefreshRotatesCookieAndLogoutRevokesFamilyAndClearsCookie(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	initialCookie := requireRefreshCookie(t, register)

	refresh := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/refresh",
		"",
		initialCookie,
	)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200; body=%s", refresh.Code, refresh.Body.String())
	}
	replacementCookie := requireRefreshCookie(t, refresh)
	if replacementCookie.Value == initialCookie.Value {
		t.Fatal("refresh cookie was not rotated")
	}
	response := decodeZTAPIAuthResponse(t, refresh)
	if response.Data.AccessToken == "" || response.Data.User.Username != "alice" {
		t.Fatalf("refresh response = %#v", response)
	}
	assertSafeAuthBody(t, refresh.Body.String(), initialCookie.Value, replacementCookie.Value)

	logout := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/logout",
		"",
		replacementCookie,
	)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200; body=%s", logout.Code, logout.Body.String())
	}
	assertClearedRefreshCookie(t, logout, false)

	var replacement model.AuthSession
	refreshHash := sha256HexForControllerTest(replacementCookie.Value)
	if err := db.Where("token_hash = ?", refreshHash).First(&replacement).Error; err != nil {
		t.Fatalf("load replacement session: %v", err)
	}
	var activeCount int64
	if err := db.Model(&model.AuthSession{}).
		Where("family_id = ? AND revoked_at IS NULL", replacement.FamilyID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active family: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("logout left %d active family sessions", activeCount)
	}

	repeat := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/logout", "", replacementCookie)
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeated logout status = %d; body=%s", repeat.Code, repeat.Body.String())
	}
	assertClearedRefreshCookie(t, repeat, false)
	withoutCookie := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/logout", "", nil)
	if withoutCookie.Code != http.StatusOK {
		t.Fatalf("cookie-less logout status = %d; body=%s", withoutCookie.Code, withoutCookie.Body.String())
	}
	assertClearedRefreshCookie(t, withoutCookie, false)
}

func TestAuthRefreshRejectsMissingExpiredRevokedAndReplayedCookies(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	initialCookie := requireRefreshCookie(t, register)
	refresh := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/refresh", "", initialCookie)
	if refresh.Code != http.StatusOK {
		t.Fatalf("rotate fixture: status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	replacementCookie := requireRefreshCookie(t, refresh)

	replay := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/refresh", "", initialCookie)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401; body=%s", replay.Code, replay.Body.String())
	}
	revoked := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/refresh", "", replacementCookie)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked replacement status = %d, want 401; body=%s", revoked.Code, revoked.Body.String())
	}
	missing := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/refresh", "", nil)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie status = %d, want 401; body=%s", missing.Code, missing.Body.String())
	}

	expiredTokens, err := service.CreateZTAPISession(
		1,
		"192.0.2.50",
		"expired-controller-test",
		time.Now().UTC().Add(-31*24*time.Hour),
	)
	if err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	expiredCookie := &http.Cookie{Name: "ztapi_refresh", Value: expiredTokens.RefreshToken}
	expired := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/refresh", "", expiredCookie)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired cookie status = %d, want 401; body=%s", expired.Code, expired.Body.String())
	}
	for _, recorder := range []*httptest.ResponseRecorder{replay, revoked, missing, expired} {
		assertSafeAuthBody(
			t,
			recorder.Body.String(),
			initialCookie.Value,
			replacementCookie.Value,
			expiredTokens.RefreshToken,
			"database",
			"stack",
		)
	}

	var familyCount int64
	if err := db.Model(&model.AuthSession{}).Count(&familyCount).Error; err != nil {
		t.Fatalf("count session rows: %v", err)
	}
	if familyCount < 3 {
		t.Fatalf("session rows = %d, expected rotation and expired fixtures", familyCount)
	}
}

func TestAuthCookieIsSecureInReleaseMode(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	gin.SetMode(gin.ReleaseMode)
	t.Cleanup(func() { gin.SetMode(gin.TestMode) })
	engine := newZTAPIAuthControllerEngine()
	register := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		nil,
	)
	if register.Code != http.StatusOK {
		t.Fatalf("release registration status = %d; body=%s", register.Code, register.Body.String())
	}
	assertIssuedRefreshCookie(t, register, true)
	logout := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/logout",
		"",
		requireRefreshCookie(t, register),
	)
	assertClearedRefreshCookie(t, logout, true)
}

func setupZTAPIAuthControllerTest(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite connection: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Token{}); err != nil {
		t.Fatalf("migrate auth controller tables: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
	if err := service.ConfigureZTAPISessionSigningKey("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("configure signing key: %v", err)
	}
	return db
}

func newZTAPIAuthControllerEngine() *gin.Engine {
	engine := gin.New()
	auth := engine.Group("/auth")
	auth.POST("/register", ZTAPIRegister)
	auth.POST("/login", ZTAPILogin)
	auth.POST("/refresh", ZTAPIRefresh)
	auth.POST("/logout", ZTAPILogout)
	auth.GET("/session", ZTAPISession)
	return engine
}

func performZTAPIAuthRequest(
	t *testing.T,
	engine *gin.Engine,
	method string,
	target string,
	body string,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.RemoteAddr = "192.0.2.25:12345"
	request.Header.Set("User-Agent", "ZTAPI Controller Test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func performZTAPIAuthRequestForHost(
	t *testing.T,
	engine *gin.Engine,
	method string,
	target string,
	body string,
	cookie *http.Cookie,
	host string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Host = host
	request.RemoteAddr = "192.0.2.25:12345"
	request.Header.Set("User-Agent", "ZTAPI Controller Test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func decodeZTAPIAuthResponse(t *testing.T, recorder *httptest.ResponseRecorder) ztAPIAuthResponse {
	t.Helper()
	var response ztAPIAuthResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode auth response: %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func requireRefreshCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "ztapi_refresh" {
			return cookie
		}
	}
	t.Fatalf("ztapi_refresh cookie missing from %v", recorder.Header().Values("Set-Cookie"))
	return nil
}

func assertIssuedRefreshCookie(t *testing.T, recorder *httptest.ResponseRecorder, secure bool) {
	t.Helper()
	cookie := requireRefreshCookie(t, recorder)
	if cookie.Value == "" {
		t.Fatal("issued refresh cookie is empty")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags = HttpOnly:%v SameSite:%v", cookie.HttpOnly, cookie.SameSite)
	}
	if cookie.Path != "/api/auth" || cookie.MaxAge != 30*24*60*60 {
		t.Fatalf("cookie scope = Path:%q MaxAge:%d", cookie.Path, cookie.MaxAge)
	}
	if cookie.Secure != secure {
		t.Fatalf("cookie Secure = %v, want %v", cookie.Secure, secure)
	}
}

func assertClearedRefreshCookie(t *testing.T, recorder *httptest.ResponseRecorder, secure bool) {
	t.Helper()
	cookie := requireRefreshCookie(t, recorder)
	if cookie.Value != "" {
		t.Fatalf("cleared cookie retained value %q", cookie.Value)
	}
	if cookie.MaxAge >= 0 {
		t.Fatalf("cleared cookie MaxAge = %d, want negative", cookie.MaxAge)
	}
	if cookie.Path != "/api/auth" || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != secure {
		t.Fatalf("cleared cookie flags = %#v", cookie)
	}
	if cookie.Expires.IsZero() || cookie.Expires.After(time.Now()) {
		t.Fatalf("cleared cookie expiry = %v, want past", cookie.Expires)
	}
}

func assertSafeAuthBody(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(strings.ToLower(body), strings.ToLower(value)) {
			t.Fatalf("response body contains sensitive/internal value %q: %s", value, body)
		}
	}
}

func assertGenericLoginFailure(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	expectedBody string,
) {
	t.Helper()
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != expectedBody {
		t.Fatalf("failure contract differs: got=%s want=%s", recorder.Body.String(), expectedBody)
	}
}

func assertReviewedZTAPIArgon2Hash(t *testing.T, encodedHash string) {
	t.Helper()
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 ||
		parts[0] != "" ||
		parts[1] != "argon2id" ||
		parts[2] != "v=19" ||
		parts[3] != "m=65536,t=3,p=2" {
		t.Fatalf("dummy hash has invalid Argon2id metadata: %q", encodedHash)
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		t.Fatalf("dummy hash salt is invalid: length=%d err=%v", len(salt), err)
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) != 32 {
		t.Fatalf("dummy hash key is invalid: length=%d err=%v", len(key), err)
	}
}

func sha256HexForControllerTest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

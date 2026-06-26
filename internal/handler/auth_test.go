package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseLang(t *testing.T) {
	tests := []struct {
		name       string
		acceptLang string
		want       string
	}{
		{name: "zh-CN", acceptLang: "zh-CN,zh;q=0.9,en;q=0.8", want: "zh"},
		{name: "zh", acceptLang: "zh", want: "zh"},
		{name: "zh-TW", acceptLang: "zh-TW,zh;q=0.9", want: "zh"},
		{name: "en-US", acceptLang: "en-US,en;q=0.9", want: "en"},
		{name: "en", acceptLang: "en", want: "en"},
		{name: "fr-FR", acceptLang: "fr-FR,fr;q=0.9", want: "en"},
		{name: "empty", acceptLang: "", want: "en"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLang(tc.acceptLang); got != tc.want {
				t.Fatalf("parseLang(%q) = %q, want %q", tc.acceptLang, got, tc.want)
			}
		})
	}
}

func TestRegister_registrationClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"email":"test@example.com","password":"test123456","name":"Test"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{registrationOpen: false}
	h.Register(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "registration is disabled on this instance" {
		t.Errorf("unexpected error: %q", resp["error"])
	}
}

func TestDeleteAccount_MissingPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/delete-account", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{}
	h.DeleteAccount(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

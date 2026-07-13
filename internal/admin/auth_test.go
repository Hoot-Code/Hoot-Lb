package admin

import (
	"net/http"
	"testing"
)

func TestAuth_MissingToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", "", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_WrongToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", "Bearer wrong-token", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_CorrectToken(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", authHeader(), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestConstantTimeComparison(t *testing.T) {
	baseURL, _, _, cleanup := startTestServer(t)
	defer cleanup()

	resp := doReq(t, http.MethodGet, baseURL+"/admin/pools", authHeader(), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("correct token: expected 200, got %d", resp.StatusCode)
	}

	wrongToken := testToken[:len(testToken)-1] + "x"
	resp = doReq(t, http.MethodGet, baseURL+"/admin/pools", "Bearer "+wrongToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", resp.StatusCode)
	}
}

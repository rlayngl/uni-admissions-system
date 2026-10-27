package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidUUID(t *testing.T) {
	valid := []string{
		"11111111-1111-4111-8111-111111111111",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	for _, value := range valid {
		if !validUUID(value) {
			t.Errorf("validUUID(%q) = false", value)
		}
	}
	invalid := []string{
		"", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaZ",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa",
	}
	for _, value := range invalid {
		if validUUID(value) {
			t.Errorf("validUUID(%q) = true", value)
		}
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	type request struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name, body string
		wantErr    bool
	}{
		{"valid", `{"name":"ok"}`, false},
		{"unknown field", `{"name":"ok","role":"ADMIN"}`, true},
		{"trailing value", `{"name":"ok"} {"name":"second"}`, true},
		{"malformed", `{"name":`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			var dst request
			err := decode(w, r, &dst)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decode error = %v; wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestArgon2idVerifier(t *testing.T) {
	pepper := []byte("this-is-a-test-pepper-of-at-least-32-bytes")
	verifier, err := hashPassword("Example-password-2026!", pepper)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(verifier, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected verifier profile: %q", verifier)
	}
	if !verifyPassword("Example-password-2026!", pepper, verifier) {
		t.Fatal("valid password was rejected")
	}
	if verifyPassword("wrong-password", pepper, verifier) {
		t.Fatal("invalid password was accepted")
	}
	if verifyPassword("Example-password-2026!", []byte("different-pepper-at-least-32-bytes"), verifier) {
		t.Fatal("wrong pepper was accepted")
	}
}

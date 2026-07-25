package admin

import "testing"

func TestLoginRequest_Validate(t *testing.T) {
	t.Run("valid request passes", func(t *testing.T) {
		req := &LoginRequest{Email: "admin@pg.id", Password: "secret"}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("email required", func(t *testing.T) {
		req := &LoginRequest{Email: "", Password: "secret"}
		if err := req.Validate(); err != ErrEmailRequired {
			t.Fatalf("expected ErrEmailRequired, got %v", err)
		}
	})

	t.Run("password required", func(t *testing.T) {
		req := &LoginRequest{Email: "admin@pg.id", Password: ""}
		if err := req.Validate(); err != ErrPasswordRequired {
			t.Fatalf("expected ErrPasswordRequired, got %v", err)
		}
	})
}

func TestChangePasswordRequest_Validate(t *testing.T) {
	valid := func() *ChangePasswordRequest {
		return &ChangePasswordRequest{CurrentPassword: "oldpass1", NewPassword: "newpass1"}
	}

	t.Run("valid request passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("current password required", func(t *testing.T) {
		req := valid()
		req.CurrentPassword = ""
		if err := req.Validate(); err != ErrCurrentPasswordRequired {
			t.Fatalf("expected ErrCurrentPasswordRequired, got %v", err)
		}
	})

	t.Run("new password required", func(t *testing.T) {
		req := valid()
		req.NewPassword = ""
		if err := req.Validate(); err != ErrNewPasswordRequired {
			t.Fatalf("expected ErrNewPasswordRequired, got %v", err)
		}
	})

	t.Run("new password must be at least 8 chars", func(t *testing.T) {
		req := valid()
		req.NewPassword = "short1"
		if err := req.Validate(); err != ErrNewPasswordTooShort {
			t.Fatalf("expected ErrNewPasswordTooShort, got %v", err)
		}
	})

	t.Run("new password must differ from current", func(t *testing.T) {
		req := valid()
		req.NewPassword = req.CurrentPassword
		if err := req.Validate(); err != ErrNewPasswordSameAsCurrent {
			t.Fatalf("expected ErrNewPasswordSameAsCurrent, got %v", err)
		}
	})
}

func TestUpdateProfileRequest_Validate(t *testing.T) {
	t.Run("name required", func(t *testing.T) {
		req := &UpdateProfileRequest{Name: "", Email: "admin@pg.id"}
		if err := req.Validate(); err != ErrNameRequired {
			t.Fatalf("expected ErrNameRequired, got %v", err)
		}
	})

	t.Run("email required", func(t *testing.T) {
		req := &UpdateProfileRequest{Name: "Admin", Email: ""}
		if err := req.Validate(); err != ErrEmailRequired {
			t.Fatalf("expected ErrEmailRequired, got %v", err)
		}
	})

	t.Run("valid request passes", func(t *testing.T) {
		req := &UpdateProfileRequest{Name: "Admin", Email: "admin@pg.id"}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

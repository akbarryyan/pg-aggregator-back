package merchant

import "testing"

func TestCreateMerchantRequest_Validate(t *testing.T) {
	valid := func() *CreateMerchantRequest {
		return &CreateMerchantRequest{
			Name:         "Toko Kopi",
			Email:        "toko@kopi.id",
			BusinessName: "Toko Kopi Sejahtera",
		}
	}

	t.Run("valid request passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("name required", func(t *testing.T) {
		req := valid()
		req.Name = ""
		if err := req.Validate(); err != ErrMerchantNameRequired {
			t.Fatalf("expected ErrMerchantNameRequired, got %v", err)
		}
	})

	t.Run("email required", func(t *testing.T) {
		req := valid()
		req.Email = ""
		if err := req.Validate(); err != ErrMerchantEmailRequired {
			t.Fatalf("expected ErrMerchantEmailRequired, got %v", err)
		}
	})

	t.Run("business name required", func(t *testing.T) {
		req := valid()
		req.BusinessName = ""
		if err := req.Validate(); err != ErrBusinessNameRequired {
			t.Fatalf("expected ErrBusinessNameRequired, got %v", err)
		}
	})
}

func TestUserLoginRequest_Validate(t *testing.T) {
	t.Run("valid request passes", func(t *testing.T) {
		req := &UserLoginRequest{Email: "a@b.com", Password: "secret"}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("blank email rejected", func(t *testing.T) {
		req := &UserLoginRequest{Email: "   ", Password: "secret"}
		if err := req.Validate(); err != ErrMerchantEmailRequired {
			t.Fatalf("expected ErrMerchantEmailRequired, got %v", err)
		}
	})

	t.Run("empty password rejected", func(t *testing.T) {
		req := &UserLoginRequest{Email: "a@b.com", Password: ""}
		if err := req.Validate(); err != ErrMerchantPasswordRequired {
			t.Fatalf("expected ErrMerchantPasswordRequired, got %v", err)
		}
	})
}

func TestUserUpdateProfileRequest_Validate(t *testing.T) {
	t.Run("blank name rejected", func(t *testing.T) {
		req := &UserUpdateProfileRequest{Name: "  ", Email: "a@b.com"}
		if err := req.Validate(); err != ErrMerchantNameRequired {
			t.Fatalf("expected ErrMerchantNameRequired, got %v", err)
		}
	})

	t.Run("blank email rejected", func(t *testing.T) {
		req := &UserUpdateProfileRequest{Name: "Budi", Email: "  "}
		if err := req.Validate(); err != ErrMerchantEmailRequired {
			t.Fatalf("expected ErrMerchantEmailRequired, got %v", err)
		}
	})
}

func TestUserChangePasswordRequest_Validate(t *testing.T) {
	valid := func() *UserChangePasswordRequest {
		return &UserChangePasswordRequest{CurrentPassword: "oldpass1", NewPassword: "newpass1"}
	}

	t.Run("valid request passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("current password required", func(t *testing.T) {
		req := valid()
		req.CurrentPassword = ""
		if err := req.Validate(); err != ErrMerchantCurrentPasswordRequired {
			t.Fatalf("expected ErrMerchantCurrentPasswordRequired, got %v", err)
		}
	})

	t.Run("new password required", func(t *testing.T) {
		req := valid()
		req.NewPassword = ""
		if err := req.Validate(); err != ErrMerchantNewPasswordRequired {
			t.Fatalf("expected ErrMerchantNewPasswordRequired, got %v", err)
		}
	})

	t.Run("new password must be at least 8 chars", func(t *testing.T) {
		req := valid()
		req.NewPassword = "short1"
		if err := req.Validate(); err != ErrMerchantNewPasswordTooShort {
			t.Fatalf("expected ErrMerchantNewPasswordTooShort, got %v", err)
		}
	})

	t.Run("new password must differ from current", func(t *testing.T) {
		req := valid()
		req.NewPassword = req.CurrentPassword
		if err := req.Validate(); err != ErrMerchantNewPasswordSame {
			t.Fatalf("expected ErrMerchantNewPasswordSame, got %v", err)
		}
	})
}
